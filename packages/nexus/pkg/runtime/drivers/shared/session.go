package shared

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
)

func NormalizeLaunchShell(shell string) string {
	s := strings.TrimSpace(shell)
	if s == "" {
		return "bash"
	}
	return s
}

func LimactlShellReconnectArgs(candidate, launchShell string) []string {
	launchShell = NormalizeLaunchShell(launchShell)
	args := []string{"shell", "--reconnect", candidate}
	if launchShell != "bash" && launchShell != "/bin/bash" {
		args = append(args, "--", launchShell)
	}
	return args
}

func ScheduleShellWorkdirCD(ptmx *os.File, workdir string) {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		_, _ = fmt.Fprintf(ptmx, "cd %s 2>/dev/null; clear\n", ShellQuote(workdir))
	}()
}

func ApplyLimaDiscovery(candidates, discovered []string, strict bool) []string {
	if len(discovered) == 0 {
		return candidates
	}
	if strict {
		return FilterCandidatesStrict(candidates, discovered)
	}
	return FilterCandidatesSortedFallback(candidates, discovered)
}

type TryLimactlPTYOptions struct {
	Candidates          []string
	LaunchShell         string
	Workdir             string
	BeforeEachCandidate func(context.Context, string) error
	PtyStart            func(*exec.Cmd, *pty.Winsize) (*os.File, error)
	ErrPrefix           string
}

func TryLimactlShellPTY(ctx context.Context, opt TryLimactlPTYOptions) (*exec.Cmd, *os.File, error) {
	launchShell := NormalizeLaunchShell(opt.LaunchShell)
	workdir := strings.TrimSpace(opt.Workdir)
	var lastErr error
	for _, candidate := range opt.Candidates {
		if opt.BeforeEachCandidate != nil {
			if err := opt.BeforeEachCandidate(ctx, candidate); err != nil {
				lastErr = err
				continue
			}
		}
		args := LimactlShellReconnectArgs(candidate, launchShell)
		cmd := exec.CommandContext(ctx, "limactl", args...)
		ptmx, ptyErr := opt.PtyStart(cmd, &pty.Winsize{Rows: 30, Cols: 120})
		if ptyErr == nil {
			ScheduleShellWorkdirCD(ptmx, workdir)
			return cmd, ptmx, nil
		}
		lastErr = ptyErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no lima instance candidates available")
	}
	prefix := strings.TrimSpace(opt.ErrPrefix)
	if prefix != "" {
		return nil, nil, fmt.Errorf("%s: %w", prefix, lastErr)
	}
	return nil, nil, fmt.Errorf("lima shell start failed: %w", lastErr)
}
