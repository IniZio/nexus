//go:build linux

package cloudhypervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	netnsProbeEnv      = "NEXUS_NETNS_TAP_PROBE"
	netnsProbeEPERM    = 3
	netnsProbeTimeout  = 15 * time.Second
	netModeProbeFile   = "netmode-probe.json"
	netModeProbeBootID = "/proc/sys/kernel/random/boot_id"
)

var tapProbeRunner = probeTapInChild

type tapProbeRecord struct {
	BootID  string `json:"boot_id"`
	Blocked bool   `json:"blocked"`
}

// ProbeTapCached reports whether the tap/bridge ops work in a throwaway
// user+net namespace. The verdict is cached per boot in dir. A blocked host
// returns an error wrapping syscall.EPERM.
func ProbeTapCached(dir string) error {
	return probeTapCached(dir, readBootID(), tapProbeRunner)
}

func probeTapCached(dir, bootID string, run func() error) error {
	path := filepath.Join(dir, netModeProbeFile)
	if bootID != "" {
		if b, err := os.ReadFile(path); err == nil {
			var rec tapProbeRecord
			if json.Unmarshal(b, &rec) == nil && rec.BootID == bootID {
				if rec.Blocked {
					return tapPermissionHint(fmt.Errorf("tap probe (cached): %w", syscall.EPERM))
				}
				return nil
			}
		}
	}
	err := run()
	blocked := err != nil && errors.Is(err, syscall.EPERM)
	if bootID != "" && (err == nil || blocked) {
		if b, merr := json.Marshal(tapProbeRecord{BootID: bootID, Blocked: blocked}); merr == nil {
			if os.MkdirAll(dir, 0o700) == nil {
				tmp := path + fmt.Sprintf(".%d.tmp", os.Getpid())
				if os.WriteFile(tmp, b, 0o600) == nil {
					if os.Rename(tmp, path) != nil {
						_ = os.Remove(tmp)
					}
				}
			}
		}
	}
	return err
}

func readBootID() string {
	b, err := os.ReadFile(netModeProbeBootID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func probeTapInChild() error {
	if os.Getenv(netnsProbeEnv) != "" {
		return errors.New("tap probe: refusing to probe from inside a probe child")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("tap probe: %w", err)
	}
	if strings.HasSuffix(filepath.Base(self), ".test") {
		return errors.New("tap probe: refusing to re-exec a test binary")
	}
	pathEnv := "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if p := os.Getenv("PATH"); p != "" {
		pathEnv = "PATH=" + p
	}
	cmd := exec.Command(self)
	cmd.Env = []string{NetnsRunEnv + "=1", netnsProbeEnv + "=1", pathEnv}
	cmd.SysProcAttr = netnsChildAttr()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tap probe: spawn: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		if werr == nil {
			return nil
		}
		var ee *exec.ExitError
		if errors.As(werr, &ee) && ee.ExitCode() == netnsProbeEPERM {
			return tapPermissionHint(fmt.Errorf("tap probe: %w", syscall.EPERM))
		}
		return fmt.Errorf("tap probe: %w: %s", werr, strings.TrimSpace(stderr.String()))
	case <-time.After(netnsProbeTimeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return errors.New("tap probe: timed out")
	}
}

// runTapProbeChild runs in the throwaway namespace: the same bridge and
// tuntap ops createTapBridge performs. The kernel drops the devices on exit.
func runTapProbeChild() {
	for _, args := range [][]string{
		{"ip", "link", "add", "nxprobe0", "type", "bridge"},
		{"ip", "tuntap", "add", "nxprobe1", "mode", "tap"},
	} {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			werr := fmt.Errorf("%s: %w: %s", args[0], err, out)
			fmt.Fprintln(os.Stderr, werr)
			if tapPermissionHint(werr) != werr {
				os.Exit(netnsProbeEPERM)
			}
			os.Exit(1)
		}
	}
	os.Exit(0)
}
