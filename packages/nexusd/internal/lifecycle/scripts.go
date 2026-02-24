package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type LifecycleScripts struct {
	ProjectPath string
}

func NewLifecycleScripts(projectPath string) *LifecycleScripts {
	return &LifecycleScripts{
		ProjectPath: projectPath,
	}
}

func (l *LifecycleScripts) lifecycleDir() string {
	return filepath.Join(l.ProjectPath, ".nexus", "lifecycle")
}

func (l *LifecycleScripts) scriptPath(name string) string {
	return filepath.Join(l.lifecycleDir(), name)
}

func (l *LifecycleScripts) scriptExists(name string) bool {
	path := l.scriptPath(name)
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func (l *LifecycleScripts) RunPreStart() error {
	return l.runScript("pre-start.sh")
}

func (l *LifecycleScripts) RunPostStart() error {
	return l.runScript("post-start.sh")
}

func (l *LifecycleScripts) RunPreStop() error {
	return l.runScript("pre-stop.sh")
}

func (l *LifecycleScripts) RunPostStop() error {
	return l.runScript("post-stop.sh")
}

func (l *LifecycleScripts) RunHealthCheck() (bool, error) {
	return l.runHealthCheckScript()
}

func (l *LifecycleScripts) runScript(name string) error {
	if !l.scriptExists(name) {
		return nil
	}

	scriptPath := l.scriptPath(name)
	cmd := exec.Command(scriptPath)
	cmd.Dir = l.ProjectPath
	cmd.Env = os.Environ()

	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start %s: %w", name, err)
	}

	proc := cmd.Process
	if proc == nil {
		return fmt.Errorf("%s process is nil", name)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s failed: %w", name, err)
		}
		return nil
	case <-time.After(30 * time.Second):
		_ = proc.Kill()
		<-done
		return fmt.Errorf("%s timed out after 30 seconds", name)
	}
}

func (l *LifecycleScripts) runHealthCheckScript() (bool, error) {
	if !l.scriptExists("health-check.sh") {
		return false, nil
	}

	scriptPath := l.scriptPath("health-check.sh")
	cmd := exec.Command(scriptPath)
	cmd.Dir = l.ProjectPath
	cmd.Env = os.Environ()

	err := cmd.Start()
	if err != nil {
		return false, fmt.Errorf("failed to start health-check: %w", err)
	}

	proc := cmd.Process
	if proc == nil {
		return false, fmt.Errorf("health-check process is nil")
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			return false, fmt.Errorf("health-check failed: %w", err)
		}
		return true, nil
	case <-time.After(10 * time.Second):
		_ = proc.Kill()
		<-done
		return false, fmt.Errorf("health-check timed out after 10 seconds")
	}
}

func (l *LifecycleScripts) HasLifecycleScripts() bool {
	dir := l.lifecycleDir()
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return info.IsDir()
}
