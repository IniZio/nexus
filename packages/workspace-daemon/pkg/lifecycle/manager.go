package lifecycle

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/config"
)

type LifecycleConfig struct {
	Version string `json:"version"`
	Hooks   Hooks  `json:"hooks"`
}

type Hooks struct {
	PreStart  []Hook `json:"pre-start"`
	PostStart []Hook `json:"post-start"`
	PreStop   []Hook `json:"pre-stop"`
	PostStop  []Hook `json:"post-stop"`
}

type Hook struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

type Manager struct {
	workspaceDir string
	config       *LifecycleConfig
}

var errNoLifecycleHooks = errors.New("no lifecycle hooks configured")

func NewManager(workspaceDir string) (*Manager, error) {
	m := &Manager{
		workspaceDir: workspaceDir,
	}

	if err := m.loadConfig(); err != nil {
		if errors.Is(err, errNoLifecycleHooks) {
			log.Printf("[lifecycle] No lifecycle config found, skipping hooks")
			return m, nil
		}
		return nil, err
	}

	log.Printf("[lifecycle] Loaded lifecycle config with %d hooks",
		len(m.config.Hooks.PreStart)+len(m.config.Hooks.PostStart)+
			len(m.config.Hooks.PreStop)+len(m.config.Hooks.PostStop))

	return m, nil
}

func (m *Manager) loadConfig() error {
	workspaceCfg, warnings, err := config.LoadWorkspaceConfig(m.workspaceDir)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		log.Printf("[lifecycle] %s", w)
	}

	autodetected, err := m.autodetectedHooks()
	if err != nil {
		return err
	}

	configuredPreStart := commandsToHooks(workspaceCfg.Lifecycle.OnSetup)
	configuredPostStart := commandsToHooks(workspaceCfg.Lifecycle.OnStart)
	configuredPreStop := commandsToHooks(workspaceCfg.Lifecycle.OnTeardown)

	preStart := configuredPreStart
	if len(preStart) == 0 {
		preStart = autodetected.PreStart
	}

	postStart := configuredPostStart
	if len(postStart) == 0 {
		postStart = autodetected.PostStart
	}

	preStop := configuredPreStop
	if len(preStop) == 0 {
		preStop = autodetected.PreStop
	}

	if len(preStart) == 0 && len(postStart) == 0 && len(preStop) == 0 {
		return errNoLifecycleHooks
	}

	m.config = &LifecycleConfig{
		Version: "v1",
		Hooks: Hooks{
			PreStart:  preStart,
			PostStart: postStart,
			PreStop:   preStop,
			PostStop:  nil,
		},
	}
	return nil
}

func (m *Manager) autodetectedHooks() (Hooks, error) {
	setupHook, err := m.autodetectedScriptHook("setup", "setup.sh")
	if err != nil {
		return Hooks{}, err
	}

	startHook, err := m.autodetectedScriptHook("start", "start.sh")
	if err != nil {
		return Hooks{}, err
	}

	teardownHook, err := m.autodetectedScriptHook("teardown", "teardown.sh")
	if err != nil {
		return Hooks{}, err
	}

	return Hooks{
		PreStart:  setupHook,
		PostStart: startHook,
		PreStop:   teardownHook,
		PostStop:  nil,
	}, nil
}

func (m *Manager) autodetectedScriptHook(name, filename string) ([]Hook, error) {
	scriptPath := filepath.Join(m.workspaceDir, ".nexus", "lifecycles", filename)
	info, err := os.Stat(scriptPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect lifecycle script %s: %w", scriptPath, err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("lifecycle script path is a directory: %s", scriptPath)
	}

	if info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("lifecycle script must be executable: %s", scriptPath)
	}

	return []Hook{{
		Name:    fmt.Sprintf("autodetected-%s", name),
		Command: "bash",
		Args:    []string{"-euo", "pipefail", scriptPath},
	}}, nil
}

func commandsToHooks(commands []string) []Hook {
	out := make([]Hook, 0, len(commands))
	for i, cmd := range commands {
		if cmd == "" {
			continue
		}
		out = append(out, Hook{
			Name:    fmt.Sprintf("hook-%d", i+1),
			Command: "sh",
			Args:    []string{"-lc", cmd},
		})
	}
	return out
}

func (m *Manager) RunPreStart() error {
	if m.config == nil {
		return nil
	}
	return m.runHooks(m.config.Hooks.PreStart, "pre-start")
}

func (m *Manager) RunPostStart() error {
	if m.config == nil {
		return nil
	}
	return m.runHooks(m.config.Hooks.PostStart, "post-start")
}

func (m *Manager) RunPreStop() error {
	if m.config == nil {
		return nil
	}
	return m.runHooks(m.config.Hooks.PreStop, "pre-stop")
}

func (m *Manager) RunPostStop() error {
	if m.config == nil {
		return nil
	}
	return m.runHooks(m.config.Hooks.PostStop, "post-stop")
}

func (m *Manager) runHooks(hooks []Hook, stage string) error {
	for _, hook := range hooks {
		log.Printf("[lifecycle] Running %s hook: %s", stage, hook.Name)

		if err := m.runHook(hook); err != nil {
			log.Printf("[lifecycle] Hook %s failed: %v", hook.Name, err)
			return fmt.Errorf("hook %s failed: %w", hook.Name, err)
		}
	}
	return nil
}

func (m *Manager) runHook(hook Hook) error {
	cmd := exec.Command(hook.Command, hook.Args...)
	cmd.Dir = m.workspaceDir

	env := os.Environ()
	for k, v := range hook.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	timeout := 30
	if hook.Timeout > 0 {
		timeout = hook.Timeout
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-time.After(time.Duration(timeout) * time.Second):
		cmd.Process.Kill()
		return fmt.Errorf("hook timed out after %d seconds", timeout)
	}

	return nil
}
