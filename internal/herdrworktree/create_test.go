package herdrworktree

import (
	"strings"
	"testing"
)

func TestValidateCreateBackend(t *testing.T) {
	t.Setenv("NEXUS_BACKEND", "")
	repo := t.TempDir()
	base := CreateArgs{RepoPath: repo, Branch: "b"}

	got, err := validateCreateBackend(base)
	if err != nil || got != "" {
		t.Fatalf("default: got %q, %v; want empty, nil", got, err)
	}

	base.Backend = "sprites"
	if got, err = validateCreateBackend(base); err != nil || got != "sprites" {
		t.Fatalf("sprites: got %q, %v", got, err)
	}

	base.Backend = "nope"
	if _, err = validateCreateBackend(base); err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("unknown backend: err = %v", err)
	}
	if err = validateCreate(base); err == nil {
		t.Fatalf("validateCreate must reject unknown backend")
	}
}
