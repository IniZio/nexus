package portfwd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Runner func(ctx context.Context, argv []string) (stdout, stderr string, exitCode int, err error)

func OSRunner(ctx context.Context, argv []string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		if ex, ok := runErr.(*exec.ExitError); ok {
			code = ex.ExitCode()
			runErr = nil
		}
	}
	return outBuf.String(), errBuf.String(), code, runErr
}

// MasterArgv returns the canonical ssh -M argv for opening a control master.
// Both Agent (local-agent path) and Forwarder (fwd-sync path) use this so
// their masters are indistinguishable and a liveness check on one socket
// never triggers a redundant second master.
func MasterArgv(target, controlPath string) []string {
	return []string{
		"ssh", "-M", "-N", "-f",
		"-o", "ControlPath=" + controlPath,
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=yes",
		"-o", "BatchMode=yes",
		"-o", "GatewayPorts=no",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		target,
	}
}

type Forwarder struct {
	ControlPath string
	SSHHost     string
	Run         Runner
}

func (f *Forwarder) MasterAlive(ctx context.Context) (bool, error) {
	_, stderr, code, err := f.Run(ctx, []string{
		"ssh", "-O", "check",
		"-o", "ControlPath=" + f.ControlPath,
		f.SSHHost,
	})
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 255:
		return false, nil
	default:
		if s := strings.TrimSpace(stderr); s != "" {
			return false, fmt.Errorf("ssh -O check: unexpected exit %d: %s", code, s)
		}
		return false, fmt.Errorf("ssh -O check: unexpected exit %d", code)
	}
}

func (f *Forwarder) EnsureMaster(ctx context.Context) error {
	alive, err := f.MasterAlive(ctx)
	if err != nil {
		return err
	}
	if alive {
		return nil
	}
	if _, statErr := os.Stat(f.ControlPath); statErr == nil {
		_ = os.Remove(f.ControlPath)
	}
	masterArgv := MasterArgv(f.SSHHost, f.ControlPath)
	_, masterStderr, code, err := f.Run(ctx, masterArgv)
	if err != nil {
		return err
	}
	if code != 0 {
		if s := strings.TrimSpace(masterStderr); s != "" {
			return fmt.Errorf("ssh master open: exit %d %s", code, s)
		}
		return fmt.Errorf("ssh master open: exit %d %v", code, masterArgv)
	}
	return nil
}

func (f *Forwarder) Apply(ctx context.Context, port uint16) error {
	spec := fmt.Sprintf("%d:127.0.0.1:%d", port, port)
	applyArgv := []string{
		"ssh", "-O", "forward",
		"-L", spec,
		"-o", "ControlPath=" + f.ControlPath,
		f.SSHHost,
	}
	_, applyStderr, code, err := f.Run(ctx, applyArgv)
	if err != nil {
		return err
	}
	if code != 0 {
		if s := strings.TrimSpace(applyStderr); s != "" {
			return fmt.Errorf("ssh -O forward: exit %d %v: %s", code, applyArgv, s)
		}
		return fmt.Errorf("ssh -O forward: exit %d %v", code, applyArgv)
	}
	return nil
}

func (f *Forwarder) Cancel(ctx context.Context, port uint16) error {
	spec := fmt.Sprintf("%d:127.0.0.1:%d", port, port)
	_, _, _, err := f.Run(ctx, []string{
		"ssh", "-O", "cancel",
		"-L", spec,
		"-o", "ControlPath=" + f.ControlPath,
		f.SSHHost,
	})
	return err
}

func (f *Forwarder) Present(ctx context.Context, port uint16) (bool, error) {
	stdout, _, code, err := f.Run(ctx, []string{"ss", "-ltn"})
	if err == nil && code == 0 {
		return portInOutput(stdout, port), nil
	}
	ssErr := err
	if ssErr == nil {
		ssErr = fmt.Errorf("ss -ltn: exit %d", code)
	}
	stdout2, _, code2, err2 := f.Run(ctx, []string{"netstat", "-an", "-p", "tcp"})
	if err2 == nil && code2 == 0 {
		return portInOutput(stdout2, port), nil
	}
	netErr := err2
	if netErr == nil {
		netErr = fmt.Errorf("netstat -an -p tcp: exit %d", code2)
	}
	return false, fmt.Errorf("ss: %v; netstat: %v", ssErr, netErr)
}

func portInOutput(output string, port uint16) bool {
	colonNeedle := fmt.Sprintf(":%d", port)
	dotNeedle := fmt.Sprintf(".%d", port)
	for _, line := range strings.Split(output, "\n") {
		if matchDotPort(line, dotNeedle) || matchColonPort(line, colonNeedle) {
			return true
		}
	}
	return false
}

func matchColonPort(line, needle string) bool {
	return hasNeedleNotFollowedByDigit(line, needle)
}

// hasNeedleNotFollowedByDigit reports whether needle occurs in line at a
// position where the next byte is not a digit, so ":80" does not match ":8080".
func hasNeedleNotFollowedByDigit(line, needle string) bool {
	start := 0
	for start < len(line) {
		idx := strings.Index(line[start:], needle)
		if idx < 0 {
			break
		}
		abs := start + idx
		after := abs + len(needle)
		if after >= len(line) || line[after] < '0' || line[after] > '9' {
			return true
		}
		start = abs + 1
	}
	return false
}

func matchDotPort(line, needle string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[len(fields)-1] != "LISTEN" {
		return false
	}
	return hasNeedleNotFollowedByDigit(line, needle)
}
