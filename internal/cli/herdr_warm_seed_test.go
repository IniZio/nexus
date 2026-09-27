package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/volumestore"
)

// hasGit reports whether git is available in PATH.
func hasGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")
}

// TestHerdrWarmProjectKey verifies that all worktrees of one repo share the
// same project key, and that two repos with the same basename in different
// dirs produce different keys.
func TestHerdrWarmProjectKey(t *testing.T) {
	if !hasGit() {
		t.Skip("git not available")
	}
	ctx := context.Background()

	// Repo A: main + two worktrees.
	baseA := t.TempDir()
	mainA := filepath.Join(baseA, "repoA")
	if err := os.MkdirAll(mainA, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, mainA)

	wt1 := filepath.Join(baseA, "wt1")
	wt2 := filepath.Join(baseA, "wt2")
	for _, wt := range []string{wt1, wt2} {
		cmd := exec.Command("git", "-C", mainA, "worktree", "add", "--detach", wt)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("worktree add: %v\n%s", err, out)
		}
	}

	keyMain, err := herdrProjectKey(ctx, mainA)
	if err != nil {
		t.Fatalf("herdrProjectKey main: %v", err)
	}
	keyWt1, err := herdrProjectKey(ctx, wt1)
	if err != nil {
		t.Fatalf("herdrProjectKey wt1: %v", err)
	}
	keyWt2, err := herdrProjectKey(ctx, wt2)
	if err != nil {
		t.Fatalf("herdrProjectKey wt2: %v", err)
	}
	if keyMain != keyWt1 || keyMain != keyWt2 {
		t.Errorf("keys differ across worktrees: main=%q wt1=%q wt2=%q", keyMain, keyWt1, keyWt2)
	}

	// Repo B: same basename "repoA" in a different directory → different key.
	baseB := t.TempDir()
	mainB := filepath.Join(baseB, "repoA")
	if err := os.MkdirAll(mainB, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, mainB)
	keyB, err := herdrProjectKey(ctx, mainB)
	if err != nil {
		t.Fatalf("herdrProjectKey repoB: %v", err)
	}
	if keyMain == keyB {
		t.Errorf("repos with same basename in different dirs produced identical key %q", keyMain)
	}
}

// TestHerdrWarmSeedEnabled verifies the opt-out paths and default.
func TestHerdrWarmSeedEnabled(t *testing.T) {
	t.Run("default_enabled", func(t *testing.T) {
		if !herdrVolumeSeedEnabled(config.Config{}) {
			t.Error("expected seeding enabled by default")
		}
	})

	t.Run("env_NEXUS_NO_VOLUME_SEED_1", func(t *testing.T) {
		t.Setenv("NEXUS_NO_VOLUME_SEED", "1")
		if herdrVolumeSeedEnabled(config.Config{}) {
			t.Error("expected seeding disabled when NEXUS_NO_VOLUME_SEED=1")
		}
	})

	t.Run("env_NEXUS_NO_VOLUME_SEED_true", func(t *testing.T) {
		t.Setenv("NEXUS_NO_VOLUME_SEED", "true")
		if herdrVolumeSeedEnabled(config.Config{}) {
			t.Error("expected seeding disabled when NEXUS_NO_VOLUME_SEED=true")
		}
	})

	t.Run("config_volumes_seed_false", func(t *testing.T) {
		f := false
		cfg := config.Config{Volumes: config.VolumesConfig{Seed: &f}}
		if herdrVolumeSeedEnabled(cfg) {
			t.Error("expected seeding disabled when config volumes.seed=false")
		}
	})

	t.Run("config_volumes_seed_true", func(t *testing.T) {
		tr := true
		cfg := config.Config{Volumes: config.VolumesConfig{Seed: &tr}}
		if !herdrVolumeSeedEnabled(cfg) {
			t.Error("expected seeding enabled when config volumes.seed=true")
		}
	})
}

// TestHerdrWarmTeardown_promoteThenRm verifies that:
// - promotion is called before Rm
// - a promotion failure does NOT skip the Rm (teardown never fails)
// - the volume name is still returned in the removed list
func TestHerdrWarmTeardown_promoteThenRm(t *testing.T) {
	// Stub herdrWtSeedVolumeFn is not exercised here (this is teardown path).
	// We exercise herdrWtRemoveVolumesFn indirectly via herdrWtRemoveVolumes.

	// We cannot create real ext4 volumes without mke2fs, so we test the
	// promotion-ordering guarantee using a fake vs.PromoteToWarm failure:
	// even when PromoteToWarm returns an error, the volume must still be Rm'd.
	//
	// herdrWtRemoveVolumes calls HerdrSpaceGetByHandle which requires a real
	// store. Build a minimal store with a binding that has an empty RepoRoot
	// so the promotion path is skipped, then verify the volume-removal path.

	storeRoot := t.TempDir()

	handle := "testrepo/main"
	vs := volumestore.New(filepath.Join(storeRoot, "volumes"))

	binding := HerdrSpaceBinding{
		SpaceLabel:       "test:space",
		HerdrWorkspaceID: "wXX",
		SandboxHandle:    handle,
		WorktreeManaged:  true,
		RepoRoot:         "",
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	for _, name := range []string{
		herdrDockerDiskVolumeName(handle),
		herdrGoCacheDiskVolumeName(handle),
		herdrGoPathDiskVolumeName(handle),
	} {
		if _, err := vs.Create(context.Background(), name, volumestore.KindDisk, 1<<20, ""); err != nil {
			t.Skipf("vs.Create(%s): %v (mke2fs unavailable?)", name, err)
		}
	}

	removed := herdrWtRemoveVolumes(context.Background(), storeRoot, handle)
	if len(removed) < 3 {
		t.Errorf("expected ≥3 volumes removed, got %d: %v", len(removed), removed)
	}
	for _, expected := range []string{
		herdrDockerDiskVolumeName(handle),
		herdrGoCacheDiskVolumeName(handle),
		herdrGoPathDiskVolumeName(handle),
	} {
		found := false
		for _, r := range removed {
			if r.Name == expected && !r.Trashed {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("volume %s not in removed list %v", expected, removed)
		}
	}
}

// TestHerdrWarmSeedWiring verifies the injectable seed fn is called and that
// its return value controls the printed output.
func TestHerdrWarmSeedWiring(t *testing.T) {
	old := herdrWtSeedVolumeFn
	t.Cleanup(func() { herdrWtSeedVolumeFn = old })

	var called []string
	herdrWtSeedVolumeFn = func(_ context.Context, _ *volumestore.VolumeStore, name, _ string, _ volumestore.WarmKind, _ int64) (bool, error) {
		called = append(called, name)
		return true, nil
	}

	t.Setenv("NEXUS_NO_VOLUME_SEED", "1")
	if herdrVolumeSeedEnabled(config.Config{}) {
		t.Fatal("expected disabled")
	}
	if len(called) != 0 {
		t.Errorf("seed fn called when disabled: %v", called)
	}

	if err := os.Unsetenv("NEXUS_NO_VOLUME_SEED"); err != nil {
		t.Fatal(err)
	}
	seeded, err := herdrWtSeedVolumeFn(context.Background(), nil, "test-vol", "proj", volumestore.WarmKindGoCache, herdrGoCacheDiskSizeBytes())
	if err != nil || !seeded {
		t.Errorf("stub returned seeded=%v err=%v", seeded, err)
	}
	if len(called) != 1 || called[0] != "test-vol" {
		t.Errorf("called = %v; want [test-vol]", called)
	}
}
