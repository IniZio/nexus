package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/config"
)

func TestLoadUserGlobal_AbsentFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.LoadUserGlobal()
	if err != nil {
		t.Fatalf("absent file: unexpected error: %v", err)
	}
	if cfg.Sandbox.Image != "" || len(cfg.Sandbox.Mounts) != 0 {
		t.Errorf("absent file: want zero Config, got %+v", cfg)
	}
}

func TestLoadUserGlobal_ValidFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "nexus3")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := []byte("version: 1\nsandbox:\n  mounts:\n    - /host/a:/guest/a\n    - source: /host/b\n      target: /guest/b\n      read_only: true\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadUserGlobal()
	if err != nil {
		t.Fatalf("valid file: %v", err)
	}
	want := []string{"/host/a:/guest/a", "/host/b:/guest/b:ro"}
	if got := []string(cfg.Sandbox.Mounts); !slicesEqual(got, want) {
		t.Errorf("Mounts = %v, want %v", got, want)
	}
}

// TestLoadUserGlobal_ImageGCKnobs guards against the parse() wiring gap where
// the image.* block was read into fileConfig but dropped from the returned
// Config (BUG-1) — the free_space_floor_gib knob was silently ignored in
// production. The values below must survive the full YAML→fileConfig→Config path.
func TestLoadUserGlobal_ImageGCKnobs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "nexus3")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := []byte("version: 1\nimage:\n  free_space_floor_gib: 42\n  keep_newest_builder_images: 3\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadUserGlobal()
	if err != nil {
		t.Fatalf("valid file: %v", err)
	}
	if cfg.Image.FreeSpaceFloorGiB != 42 {
		t.Errorf("Image.FreeSpaceFloorGiB = %d, want 42 (config knob dropped by parse())", cfg.Image.FreeSpaceFloorGiB)
	}
	if cfg.Image.KeepNewestBuilderImages != 3 {
		t.Errorf("Image.KeepNewestBuilderImages = %d, want 3", cfg.Image.KeepNewestBuilderImages)
	}
}

// TestLoadUserGlobal_BuilderMemoryKnob guards against the parse() wiring gap
// where the builder.* block is read into fileConfig but dropped from the
// returned Config (mirrors BUG-1 for the builder knob). The value below must
// survive the full YAML→fileConfig→Config path.
func TestLoadUserGlobal_BuilderMemoryKnob(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "nexus3")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := []byte("version: 1\nbuilder:\n  memory_mib: 4096\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadUserGlobal()
	if err != nil {
		t.Fatalf("valid file: %v", err)
	}
	if cfg.Builder.MemoryMiB != 4096 {
		t.Errorf("Builder.MemoryMiB = %d, want 4096 (builder knob dropped by parse())", cfg.Builder.MemoryMiB)
	}
}

func TestLoadUserGlobal_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "nexus3")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Missing version field — parse rejects this.
	data := []byte("sandbox:\n  image: foo\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.LoadUserGlobal()
	if err == nil {
		t.Fatal("malformed file: expected error, got nil")
	}
}
