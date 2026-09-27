package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/IniZio/nexus/internal/controller"
)

// CLIConfig configures a CLILifecycle.
type CLIConfig struct {
	// NexusBin is the path to the nexus binary.
	// Falls back to NEXUS_BIN env, PATH lookup, then os.Executable.
	NexusBin string

	// ExtraEnv is appended to filteredOSEnv. Ignored when FullEnv is non-nil.
	ExtraEnv []string

	// FullEnv, when non-nil, is used as the complete subprocess environment
	// instead of filteredOSEnv+ExtraEnv.
	FullEnv []string
}

// CLILifecycle implements controller.SandboxLifecycle by shelling out to the nexus CLI.
type CLILifecycle struct {
	nexusBin string
	env      []string
	run      func(ctx context.Context, argv []string) (string, error)
}

var _ controller.SandboxLifecycle = (*CLILifecycle)(nil)

// NewCLILifecycle returns a CLILifecycle configured from cfg.
func NewCLILifecycle(cfg CLIConfig) *CLILifecycle {
	bin := cfg.NexusBin
	if bin == "" {
		bin = os.Getenv("NEXUS_BIN")
	}
	if bin == "" {
		if p, err := exec.LookPath("nexus"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		bin, _ = os.Executable()
	}

	var env []string
	if cfg.FullEnv != nil {
		env = cfg.FullEnv
	} else {
		env = append(filteredOSEnv(), cfg.ExtraEnv...)
	}

	lc := &CLILifecycle{nexusBin: bin, env: env}
	lc.run = lc.execRun
	return lc
}

func (l *CLILifecycle) execRun(ctx context.Context, argv []string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = l.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (l *CLILifecycle) Pause(ctx context.Context, sandboxID string) error {
	out, err := l.run(ctx, []string{l.nexusBin, "herdr", "pause", sandboxID})
	if err != nil {
		return fmt.Errorf("nexus herdr pause %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

func (l *CLILifecycle) Resume(ctx context.Context, sandboxID string) error {
	out, err := l.run(ctx, []string{l.nexusBin, "herdr", "resume", sandboxID})
	if err != nil {
		return fmt.Errorf("nexus herdr resume %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

func (l *CLILifecycle) Stop(ctx context.Context, sandboxID string) error {
	out, err := l.run(ctx, []string{l.nexusBin, "sandbox", "stop", sandboxID})
	if err != nil {
		return fmt.Errorf("nexus sandbox stop %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

func (l *CLILifecycle) Start(ctx context.Context, sandboxID string) error {
	out, err := l.run(ctx, []string{l.nexusBin, "sandbox", "start", sandboxID})
	if err != nil {
		return fmt.Errorf("nexus sandbox start %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

// filteredOSEnv returns os.Environ() with herdr/claude session vars stripped
// so child processes do not inherit the launching shell's pane/workspace context.
func filteredOSEnv() []string {
	skipPrefixes := []string{
		"HERDR_PANE_ID=", "HERDR_TAB_ID=", "HERDR_WORKSPACE_ID=",
		"HERDR_ENV=", "CLAUDECODE=", "CLAUDE_CODE_",
	}
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(kv, p) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}
