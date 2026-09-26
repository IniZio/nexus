package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/IniZio/nexus/internal/core/domain"
)

// agentCfgStage holds an exclusive flock on agentcfg-lower so the reaper can distinguish in-progress creates from orphans.
type agentCfgStage struct {
	Dir  string   // <disksDir>/<id>-agentcfg-lower
	lock *os.File // dir fd holding LOCK_EX; nil after Finish
}

// beginAgentCfgStage creates the agentcfg-lower directory already leased (lease-before-visibility).
func beginAgentCfgStage(disksDir string, id domain.SandboxID) (*agentCfgStage, error) {
	if err := os.MkdirAll(disksDir, 0o700); err != nil {
		return nil, fmt.Errorf("agentcfg stage: mkdir disksDir: %w", err)
	}

	final := filepath.Join(disksDir, id.String()+"-agentcfg-lower")
	tmp := final + ".staging"

	if err := os.Mkdir(tmp, 0o755); err != nil {
		return nil, fmt.Errorf("agentcfg stage: mkdir staging: %w", err)
	}

	f, err := os.Open(tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("agentcfg stage: open staging: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("agentcfg stage: flock staging: %w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("agentcfg stage: rename to final: %w", err)
	}

	return &agentCfgStage{Dir: final, lock: f}, nil
}

// Finish releases the stage, removing the directory on createErr; nil receiver is a no-op; idempotent.
func (s *agentCfgStage) Finish(createErr error) {
	if s == nil || s.lock == nil {
		return
	}
	if createErr != nil {
		_ = os.RemoveAll(s.Dir)
	}
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	_ = s.lock.Close()
	s.lock = nil
}

var errAgentCfgStageAbandoned = errors.New("agentcfg stage abandoned before create finished")
