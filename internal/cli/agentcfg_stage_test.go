package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func newTestID(t *testing.T) domain.SandboxID {
	t.Helper()
	id, err := domain.NewSandboxID(), error(nil)
	_ = err
	return id
}

// probeExclusive tries to take LOCK_EX|LOCK_NB on dir and returns the error.
func probeExclusive(t *testing.T, dir string) error {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func TestAgentCfgStage_BeginCreatesLockedDir(t *testing.T) {
	disksDir := t.TempDir()
	id := domain.NewSandboxID()

	stage, err := beginAgentCfgStage(disksDir, id)
	if err != nil {
		t.Fatalf("beginAgentCfgStage: %v", err)
	}
	defer stage.Finish(errors.New("cleanup"))

	// final dir must exist
	if _, err := os.Stat(stage.Dir); err != nil {
		t.Fatalf("final dir missing: %v", err)
	}

	// no .staging remnant
	if _, err := os.Stat(stage.Dir + ".staging"); !os.IsNotExist(err) {
		t.Fatal("staging dir still present after begin")
	}

	// probe must get EWOULDBLOCK while stage holds the lock
	if err := probeExclusive(t, stage.Dir); err != syscall.EWOULDBLOCK {
		t.Fatalf("probe: want EWOULDBLOCK, got %v", err)
	}
}

func TestAgentCfgStage_FinishNil_KeepsDir(t *testing.T) {
	disksDir := t.TempDir()
	id := domain.NewSandboxID()

	stage, err := beginAgentCfgStage(disksDir, id)
	if err != nil {
		t.Fatalf("beginAgentCfgStage: %v", err)
	}

	stage.Finish(nil)

	// dir must still exist
	if _, err := os.Stat(stage.Dir); err != nil {
		t.Fatalf("dir gone after Finish(nil): %v", err)
	}

	// lock must be released — probe should succeed
	if err := probeExclusive(t, stage.Dir); err != nil {
		t.Fatalf("probe after Finish(nil): want nil, got %v", err)
	}
}

func TestAgentCfgStage_FinishCtxCanceled_RemovesDir(t *testing.T) {
	disksDir := t.TempDir()
	id := domain.NewSandboxID()

	stage, err := beginAgentCfgStage(disksDir, id)
	if err != nil {
		t.Fatalf("beginAgentCfgStage: %v", err)
	}

	// plant a nested file to verify RemoveAll
	nested := filepath.Join(stage.Dir, "some-file")
	if err := os.WriteFile(nested, []byte("data"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	stage.Finish(context.Canceled)

	if _, err := os.Stat(stage.Dir); !os.IsNotExist(err) {
		t.Fatal("dir still present after Finish(context.Canceled)")
	}
}

func TestAgentCfgStage_FinishError_RemovesDir(t *testing.T) {
	disksDir := t.TempDir()
	id := domain.NewSandboxID()

	stage, err := beginAgentCfgStage(disksDir, id)
	if err != nil {
		t.Fatalf("beginAgentCfgStage: %v", err)
	}

	nested := filepath.Join(stage.Dir, "some-file")
	if err := os.WriteFile(nested, []byte("data"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	stage.Finish(errors.New("create failed"))

	if _, err := os.Stat(stage.Dir); !os.IsNotExist(err) {
		t.Fatal("dir still present after Finish(error)")
	}
}

func TestAgentCfgStage_FinishTwice_NoPanic(t *testing.T) {
	disksDir := t.TempDir()
	id := domain.NewSandboxID()

	stage, err := beginAgentCfgStage(disksDir, id)
	if err != nil {
		t.Fatalf("beginAgentCfgStage: %v", err)
	}

	stage.Finish(nil)
	stage.Finish(nil) // must not panic
}

func TestAgentCfgStage_NilReceiver_NoPanic(t *testing.T) {
	var s *agentCfgStage
	s.Finish(nil)
	s.Finish(errors.New("x"))
}

func TestAgentCfgStage_BeginDisksNotDir_Errors(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	id := domain.NewSandboxID()
	_, err := beginAgentCfgStage(filePath, id)
	if err == nil {
		t.Fatal("want error when disksDir is a file, got nil")
	}

}
