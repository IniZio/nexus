//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func TestSandboxToolsDir_Present(t *testing.T) {
	dir := t.TempDir()
	orig := sandboxToolsTreeDir
	sandboxToolsTreeDir = dir
	t.Cleanup(func() { sandboxToolsTreeDir = orig })

	got := builderSandboxToolsDir()
	if got != dir {
		t.Fatalf("expected %q, got %q", dir, got)
	}
}

func TestSandboxToolsDir_Absent(t *testing.T) {
	orig := sandboxToolsTreeDir
	sandboxToolsTreeDir = filepath.Join(t.TempDir(), "nonexistent")
	t.Cleanup(func() { sandboxToolsTreeDir = orig })

	got := builderSandboxToolsDir()
	if got != "" {
		t.Fatalf("expected empty string for absent dir, got %q", got)
	}
}

func TestSandboxToolsDir_FileNotDir(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notadir")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	orig := sandboxToolsTreeDir
	sandboxToolsTreeDir = f.Name()
	t.Cleanup(func() { sandboxToolsTreeDir = orig })

	got := builderSandboxToolsDir()
	if got != "" {
		t.Fatalf("expected empty string for file (not dir), got %q", got)
	}
}

// TestNewInGuestSolveRequest_SandboxToolsDir_Present verifies that when
// sandboxToolsTreeDir points to an existing directory, newInGuestSolveRequest
// maps it to SandboxToolsDir.
func TestNewInGuestSolveRequest_SandboxToolsDir_Present(t *testing.T) {
	toolsDir := t.TempDir()
	orig := sandboxToolsTreeDir
	sandboxToolsTreeDir = toolsDir
	t.Cleanup(func() { sandboxToolsTreeDir = orig })

	req := newInGuestSolveRequest("ubuntu:24.04", InGuestBuildOptions{})
	if req.SandboxToolsDir != toolsDir {
		t.Fatalf("SandboxToolsDir: got %q, want %q", req.SandboxToolsDir, toolsDir)
	}
}

// TestNewInGuestSolveRequest_SandboxToolsDir_Absent verifies that when
// sandboxToolsTreeDir does not exist, SandboxToolsDir is "".
func TestNewInGuestSolveRequest_SandboxToolsDir_Absent(t *testing.T) {
	orig := sandboxToolsTreeDir
	sandboxToolsTreeDir = filepath.Join(t.TempDir(), "nonexistent")
	t.Cleanup(func() { sandboxToolsTreeDir = orig })

	req := newInGuestSolveRequest("ubuntu:24.04", InGuestBuildOptions{})
	if req.SandboxToolsDir != "" {
		t.Fatalf("SandboxToolsDir: got %q, want empty", req.SandboxToolsDir)
	}
}

// TestNewInGuestSolveRequest_FixedFields pins that newInGuestSolveRequest maps
// all other SolveRequest fields correctly so the refactor is detected by tests
// if any field is dropped or miswired.
func TestNewInGuestSolveRequest_FixedFields(t *testing.T) {
	toolsDir := t.TempDir()
	orig := sandboxToolsTreeDir
	sandboxToolsTreeDir = toolsDir
	t.Cleanup(func() { sandboxToolsTreeDir = orig })

	recipe := cred.ToolRecipe{Packages: []cred.RecipePackage{{Name: "gh"}}}
	opts := InGuestBuildOptions{
		ContainerfileBytes: []byte("FROM ubuntu:24.04\nRUN echo hi"),
		AgentPath:          "/tmp/nexus-agent",
		ContextDir:         "/tmp/ctx",
		ToolRecipe:         recipe,
		TargetArch:         "x64",
	}

	req := newInGuestSolveRequest("ubuntu:24.04", opts)

	if req.BaseRef != "ubuntu:24.04" {
		t.Errorf("BaseRef: got %q, want %q", req.BaseRef, "ubuntu:24.04")
	}
	if string(req.ContainerfileBytes) != string(opts.ContainerfileBytes) {
		t.Errorf("ContainerfileBytes: got %q, want %q", req.ContainerfileBytes, opts.ContainerfileBytes)
	}
	if req.AgentPath != opts.AgentPath {
		t.Errorf("AgentPath: got %q, want %q", req.AgentPath, opts.AgentPath)
	}
	if req.AgentInstallPath != "/sbin/nexus-agent" {
		t.Errorf("AgentInstallPath: got %q, want /sbin/nexus-agent", req.AgentInstallPath)
	}
	if req.WorkspaceDir != opts.ContextDir {
		t.Errorf("WorkspaceDir: got %q, want %q (opts.ContextDir)", req.WorkspaceDir, opts.ContextDir)
	}
	if req.TargetArch != opts.TargetArch {
		t.Errorf("TargetArch: got %q, want %q", req.TargetArch, opts.TargetArch)
	}
	if req.SandboxToolsDir != toolsDir {
		t.Errorf("SandboxToolsDir: got %q, want %q", req.SandboxToolsDir, toolsDir)
	}
}
