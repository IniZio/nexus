// Package shared – direct SSH helpers for Lima-backed runtimes.
//
// Rather than spawning `limactl shell` (a Go wrapper that adds log noise and
// process-spawn overhead), we read the ssh.config Lima already writes at
// ~/.lima/INSTANCE/ssh.config and exec ssh directly.
// The ControlMaster socket Lima keeps open means the first SSH connection
// does the key exchange; all subsequent connections are instant mux reuse.
package shared

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
)

// limaHome returns the Lima home directory, respecting $LIMA_HOME.
func limaHome() (string, error) {
	if d := os.Getenv("LIMA_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".lima"), nil
}

// limaSSHConfigPath returns ~/.lima/INSTANCE/ssh.config (Lima-generated).
func limaSSHConfigPath(instanceName string) (string, error) {
	dir, err := limaHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, instanceName, "ssh.config"), nil
}

// nexusSSHConfigPath returns a nexus-managed SSH config for instanceName.
// It rewrites Lima's "Host lima-INSTANCE" to plain "Host INSTANCE" so all
// ssh invocations use the bare instance name (e.g. "nexus") rather than
// "lima-nexus".  The file is regenerated each call so port changes after a
// Lima restart are always picked up.
func nexusSSHConfigPath(instanceName string) (string, error) {
	limaCfg, err := limaSSHConfigPath(instanceName)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(limaCfg)
	if err != nil {
		return "", fmt.Errorf("lima ssh.config not found for %q: %w", instanceName, err)
	}

	// Replace "Host lima-INSTANCE" → "Host INSTANCE" (keeps the same block).
	rewritten := strings.ReplaceAll(string(raw), "Host lima-"+instanceName, "Host "+instanceName)

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".nexus", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	out := filepath.Join(dir, instanceName+".ssh.config")
	if err := os.WriteFile(out, []byte(rewritten), 0o600); err != nil {
		return "", err
	}
	return out, nil
}

// DirectSSHInteractiveArgs returns the ssh(1) argument slice that opens an
// interactive login shell inside the Lima instance, starting in workdir.
//
// Equivalent to what `limactl shell --reconnect --workdir WORKDIR INSTANCE`
// does internally, but without the limactl wrapper overhead or log noise.
func DirectSSHInteractiveArgs(instanceName, workdir, shell string) ([]string, error) {
	cfgPath, err := nexusSSHConfigPath(instanceName)
	if err != nil {
		return nil, err
	}

	sh := strings.TrimSpace(shell)
	if sh == "" {
		sh = "bash"
	}

	wd := strings.TrimSpace(workdir)
	if wd != "" {
		innerCmd := `export PROMPT_COMMAND="cd ` + ShellQuote(wd) + ` 2>/dev/null; unset PROMPT_COMMAND"; exec ` + sh + ` -i`
		fullCmd := sh + " -l -c " + ShellQuote(innerCmd)
		return []string{
			"-F", cfgPath,
			"-t", // force remote PTY allocation; required when a command is given
			instanceName,
			fullCmd,
		}, nil
	}

	// No workdir: pass "bash -l" as a single arg so SSH executes it directly.
	return []string{
		"-F", cfgPath,
		"-t",
		instanceName,
		sh + " -l",
	}, nil
}

// DirectSSHScriptArgs returns the ssh(1) argument slice that runs a
// non-interactive shell script inside the Lima instance.
//
// Equivalent to `limactl shell INSTANCE -- sh -lc SCRIPT`.
func DirectSSHScriptArgs(instanceName, script string) ([]string, error) {
	cfgPath, err := nexusSSHConfigPath(instanceName)
	if err != nil {
		return nil, err
	}

	return []string{
		"-F", cfgPath,
		instanceName,
		"--",
		"sh", "-lc", script,
	}, nil
}

// DirectSSHScript runs script inside instanceName via direct SSH and returns
// combined stdout+stderr. Drop-in replacement for LimactlShellScript.
func DirectSSHScript(ctx context.Context, instanceName, script string) ([]byte, error) {
	args, err := DirectSSHScriptArgs(instanceName, script)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
}

// TryDirectSSHScript runs script in the first candidate Lima instance whose
// ssh.config exists and the SSH command succeeds.  Use this wherever a single
// instance name is not guaranteed (e.g. after a daemon restart where the
// stored instance name may be a legacy alias).
func TryDirectSSHScript(ctx context.Context, candidates []string, script string) ([]byte, error) {
	var lastErr error
	for _, candidate := range candidates {
		out, err := DirectSSHScript(ctx, candidate, script)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		return nil, fmt.Errorf("no lima instance candidates available")
	}
	return nil, lastErr
}

// TryDirectSSHShellPTY attempts to start an interactive PTY shell in each
// Lima instance candidate via direct SSH (no limactl wrapper).
// It is a drop-in replacement for TrySSHShellPTY.
func TryDirectSSHShellPTY(ctx context.Context, opt TrySSHPTYOptions) (*exec.Cmd, *os.File, error) {
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

		args, err := DirectSSHInteractiveArgs(candidate, workdir, launchShell)
		if err != nil {
			lastErr = err
			continue
		}

		cmd := exec.CommandContext(ctx, "ssh", args...)
		ptmx, ptyErr := opt.PtyStart(cmd, &pty.Winsize{Rows: 30, Cols: 120})
		if ptyErr == nil {
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
	return nil, nil, fmt.Errorf("lima ssh shell start failed: %w", lastErr)
}
