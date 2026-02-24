package lifecycle

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

func NewManager(workspaceDir string) (*Manager, error) {
	m := &Manager{
		workspaceDir: workspaceDir,
	}

	if err := m.loadConfig(); err != nil {
		log.Printf("[lifecycle] No lifecycle config found, skipping hooks")
		return m, nil
	}

	log.Printf("[lifecycle] Loaded lifecycle config with %d hooks",
		len(m.config.Hooks.PreStart)+len(m.config.Hooks.PostStart)+
			len(m.config.Hooks.PreStop)+len(m.config.Hooks.PostStop))

	return m, nil
}

func (m *Manager) loadConfig() error {
	configPath := filepath.Join(m.workspaceDir, ".nexus", "lifecycle.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	var config LifecycleConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse lifecycle.json: %w", err)
	}

	m.config = &config
	return nil
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
		_ = cmd.Process.Kill()
		return fmt.Errorf("hook timed out after %d seconds", timeout)
	}

	return nil
}
