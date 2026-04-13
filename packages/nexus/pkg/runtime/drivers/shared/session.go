package shared

import (
	"context"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func NormalizeLaunchShell(shell string) string {
	if shell == "" {
		return "bash"
	}
	return shell
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

type TrySSHPTYOptions struct {
	Candidates          []string
	LaunchShell         string
	Workdir             string
	BeforeEachCandidate func(context.Context, string) error
	PtyStart            func(*exec.Cmd, *pty.Winsize) (*os.File, error)
	ErrPrefix           string
}

// TrySSHShellPTY opens an interactive PTY shell in the first reachable
// Lima instance candidate via direct SSH.
func TrySSHShellPTY(ctx context.Context, opt TrySSHPTYOptions) (*exec.Cmd, *os.File, error) {
	return TryDirectSSHShellPTY(ctx, opt)
}
