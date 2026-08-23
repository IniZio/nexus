package nexusfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/cli/nexusfile"
)

// writeNexusfile writes content to a temp dir and returns the file path.
func writeNexusfile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Nexusfile")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp Nexusfile: %v", err)
	}
	return path
}

const validNexusfile = `
"$schema" = "./schemas/nexusfile.schema.json"

[dev]
bake = ["apt-get update -q", "apt-get install -y docker-compose-plugin"]
up   = ["docker compose build --parallel", "docker compose up -d"]
down = ["docker compose down"]

[staging]
bake = ["echo staging-bake"]
up   = ["echo staging-up"]
down = ["echo staging-down"]
`

func TestLoad_ValidFile_DefaultSection(t *testing.T) {
	path := writeNexusfile(t, validNexusfile)
	nf, err := nexusfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sec, err := nf.Section("dev")
	if err != nil {
		t.Fatalf("Section(dev): %v", err)
	}
	if len(sec.Bake) != 2 {
		t.Errorf("Bake: want 2 commands, got %d", len(sec.Bake))
	}
	if sec.Bake[0] != "apt-get update -q" {
		t.Errorf("Bake[0]: want %q, got %q", "apt-get update -q", sec.Bake[0])
	}
	if len(sec.Up) != 2 {
		t.Errorf("Up: want 2 commands, got %d", len(sec.Up))
	}
	if len(sec.Down) != 1 {
		t.Errorf("Down: want 1 command, got %d", len(sec.Down))
	}
	if sec.Down[0] != "docker compose down" {
		t.Errorf("Down[0]: want %q, got %q", "docker compose down", sec.Down[0])
	}
}

func TestLoad_ValidFile_ExplicitSection(t *testing.T) {
	path := writeNexusfile(t, validNexusfile)
	nf, err := nexusfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sec, err := nf.Section("staging")
	if err != nil {
		t.Fatalf("Section(staging): %v", err)
	}
	if len(sec.Bake) != 1 || sec.Bake[0] != "echo staging-bake" {
		t.Errorf("staging Bake: got %v", sec.Bake)
	}
}

func TestLoad_MissingSection(t *testing.T) {
	path := writeNexusfile(t, validNexusfile)
	nf, err := nexusfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = nf.Section("nonexistent")
	if !errors.Is(err, nexusfile.ErrSectionNotFound) {
		t.Fatalf("want ErrSectionNotFound, got %v", err)
	}
}

func TestLoad_MalformedTOML(t *testing.T) {
	path := writeNexusfile(t, `[dev
bake = [not valid toml`)
	_, err := nexusfile.Load(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML, got nil")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := nexusfile.Load("/nonexistent/path/Nexusfile")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoad_SchemaKeySkipped(t *testing.T) {
	// "$schema" is a scalar key — must not be treated as a section.
	path := writeNexusfile(t, validNexusfile)
	nf, err := nexusfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// "$schema" should NOT appear as a section.
	_, err = nf.Section("$schema")
	if !errors.Is(err, nexusfile.ErrSectionNotFound) {
		t.Fatalf("want $schema to be absent, got section or wrong error: %v", err)
	}
}

func TestLoad_MultilineArray(t *testing.T) {
	content := `
[dev]
bake = [
  "step one",
  "step two",
  "step three",
]
`
	path := writeNexusfile(t, content)
	nf, err := nexusfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sec, err := nf.Section("dev")
	if err != nil {
		t.Fatalf("Section: %v", err)
	}
	if len(sec.Bake) != 3 {
		t.Errorf("Bake: want 3 commands, got %d: %v", len(sec.Bake), sec.Bake)
	}
}
