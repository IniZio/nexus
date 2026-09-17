package portfwd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
	_, stderr, code, err := f.Run(ctx, []string{
		"ssh", "-O", "cancel",
		"-L", spec,
		"-o", "ControlPath=" + f.ControlPath,
		f.SSHHost,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		if s := strings.TrimSpace(stderr); s != "" {
			return fmt.Errorf("ssh -O cancel: exit %d: %s", code, s)
		}
		return fmt.Errorf("ssh -O cancel: exit %d", code)
	}
	return nil
}

// Presence is the three-way answer to "is local :port bound?". A port bound
// by our own ControlMaster (pid from `ssh -O check`) is a forward a previous
// client left on the surviving master, not a foreign conflict.
type Presence int

const (
	PresenceAbsent Presence = iota
	PresenceOurs
	PresenceForeign
)

func (p Presence) String() string {
	switch p {
	case PresenceAbsent:
		return "absent"
	case PresenceOurs:
		return "ours"
	default:
		return "foreign"
	}
}

var pidRe = regexp.MustCompile(`pid=(\d+)`)

// MasterPID returns the ControlMaster pid reported by `ssh -O check`
// ("Master running (pid=N)"), or 0 when no master is running.
func (f *Forwarder) MasterPID(ctx context.Context) (int, error) {
	_, stderr, code, err := f.Run(ctx, []string{
		"ssh", "-O", "check",
		"-o", "ControlPath=" + f.ControlPath,
		f.SSHHost,
	})
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, nil
	}
	m := pidRe.FindStringSubmatch(stderr)
	if m == nil {
		return 0, fmt.Errorf("ssh -O check: no pid in %q", strings.TrimSpace(stderr))
	}
	pid, _ := strconv.Atoi(m[1])
	return pid, nil
}

func (f *Forwarder) Present(ctx context.Context, port uint16) (Presence, error) {
	stdout, _, code, err := f.Run(ctx, []string{"ss", "-ltnp"})
	if err == nil && code == 0 {
		line, ok := portLine(stdout, port)
		if !ok {
			return PresenceAbsent, nil
		}
		return f.classifyOwner(ctx, ownerPIDsFromSS(line))
	}
	ssErr := err
	if ssErr == nil {
		ssErr = fmt.Errorf("ss -ltnp: exit %d", code)
	}
	stdout2, _, code2, err2 := f.Run(ctx, []string{"netstat", "-an", "-p", "tcp"})
	if err2 == nil && code2 == 0 {
		if _, ok := portLine(stdout2, port); !ok {
			return PresenceAbsent, nil
		}
		lsofOut, _, lsofCode, lsofErr := f.Run(ctx, []string{
			"lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-Fp",
		})
		if lsofErr != nil || lsofCode != 0 {
			return PresenceForeign, nil
		}
		return f.classifyOwner(ctx, lsofFieldValues(lsofOut, 'p'))
	}
	netErr := err2
	if netErr == nil {
		netErr = fmt.Errorf("netstat -an -p tcp: exit %d", code2)
	}
	return PresenceAbsent, fmt.Errorf("ss: %v; netstat: %v", ssErr, netErr)
}

func (f *Forwarder) classifyOwner(ctx context.Context, owners []int) (Presence, error) {
	if len(owners) == 0 {
		return PresenceForeign, nil
	}
	master, err := f.MasterPID(ctx)
	if err != nil {
		return PresenceForeign, err
	}
	for _, pid := range owners {
		if master != 0 && pid == master {
			return PresenceOurs, nil
		}
	}
	return PresenceForeign, nil
}

// MasterForwards lists the local ports our ControlMaster is listening on —
// its -L forwards — so a restarted client can adopt them. Empty when no
// master is running.
func (f *Forwarder) MasterForwards(ctx context.Context) ([]uint16, error) {
	master, err := f.MasterPID(ctx)
	if err != nil || master == 0 {
		return nil, err
	}
	needle := fmt.Sprintf("pid=%d,", master)
	stdout, _, code, err := f.Run(ctx, []string{"ss", "-ltnp"})
	if err == nil && code == 0 {
		var ports []uint16
		for _, line := range strings.Split(stdout, "\n") {
			if !strings.Contains(line, needle) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			if p, ok := trailingPort(fields[3], ':'); ok {
				ports = appendUniquePort(ports, p)
			}
		}
		return ports, nil
	}
	lsofOut, _, lsofCode, lsofErr := f.Run(ctx, []string{
		"lsof", "-nP", "-a", "-p", strconv.Itoa(master), "-iTCP", "-sTCP:LISTEN", "-Fn",
	})
	if lsofErr != nil {
		return nil, fmt.Errorf("ss: %v; lsof: %v", err, lsofErr)
	}
	if lsofCode != 0 {
		return nil, nil
	}
	var ports []uint16
	for _, line := range strings.Split(lsofOut, "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		if p, ok := trailingPort(line[1:], ':'); ok {
			ports = appendUniquePort(ports, p)
		}
	}
	return ports, nil
}

func appendUniquePort(ports []uint16, p uint16) []uint16 {
	for _, q := range ports {
		if q == p {
			return ports
		}
	}
	return append(ports, p)
}

func trailingPort(addr string, sep byte) (uint16, bool) {
	idx := strings.LastIndexByte(addr, sep)
	if idx < 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(addr[idx+1:], 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(n), true
}

func ownerPIDsFromSS(line string) []int {
	var pids []int
	for _, m := range pidRe.FindAllStringSubmatch(line, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// lsofFieldValues parses `lsof -F` output: one field per line, the first
// byte is the field tag.
func lsofFieldValues(output string, tag byte) []int {
	var vals []int
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 2 || line[0] != tag {
			continue
		}
		if n, err := strconv.Atoi(line[1:]); err == nil {
			vals = append(vals, n)
		}
	}
	return vals
}

func portInOutput(output string, port uint16) bool {
	_, ok := portLine(output, port)
	return ok
}

func portLine(output string, port uint16) (string, bool) {
	colonNeedle := fmt.Sprintf(":%d", port)
	dotNeedle := fmt.Sprintf(".%d", port)
	for _, line := range strings.Split(output, "\n") {
		if matchDotPort(line, dotNeedle) || matchColonPort(line, colonNeedle) {
			return line, true
		}
	}
	return "", false
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
