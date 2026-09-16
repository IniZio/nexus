package cli

// Tests for `space-prune --workspace <id>` (the worktree.removed hook path).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// fakePruneSvc is a herdrSpacePruneLister that also implements
// HerdrSpaceSandboxService, so herdrPluginSpacePrune wires the real
// removeSandbox path to it.
type fakePruneSvc struct {
	fakeSandboxSvc
	sbs []domain.Sandbox
}

func (f *fakePruneSvc) List(_ context.Context) ([]domain.Sandbox, error) { return f.sbs, nil }

func TestHerdrSpacePrune_WorkspaceScoped_ReapsOnlyNamedBinding(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	closed := HerdrSpaceBinding{
		SpaceLabel: "nexus3:repo/closed", HerdrWorkspaceID: "wCLOSED",
		SandboxHandle: "repo/closed", SandboxID: "sb-closed", WorktreeManaged: true,
	}
	other := HerdrSpaceBinding{
		SpaceLabel: "nexus3:repo/other", HerdrWorkspaceID: "wOTHER",
		SandboxHandle: "repo/other", SandboxID: "sb-other", WorktreeManaged: true,
	}
	for _, b := range []HerdrSpaceBinding{closed, other} {
		if err := HerdrSpacePut(ctx, root, b); err != nil {
			t.Fatalf("HerdrSpacePut(%s): %v", b.SpaceLabel, err)
		}
	}

	svc := &fakePruneSvc{sbs: []domain.Sandbox{
		{Project: "repo", Name: "closed"},
		{Project: "repo", Name: "other"},
	}}

	var buf bytes.Buffer
	if err := herdrPluginSpacePrune(ctx, []string{"--apply", "--workspace", "wCLOSED"}, &buf, svc, root, "/bin/true"); err != nil {
		t.Fatalf("herdrPluginSpacePrune --workspace: %v", err)
	}

	if got := svc.removed; len(got) != 1 || got[0] != "repo/closed" {
		t.Errorf("Remove calls = %v; want [repo/closed]", got)
	}
	if _, err := HerdrSpaceGetByLabel(ctx, root, closed.SpaceLabel); err == nil {
		t.Errorf("binding %s must be deleted", closed.SpaceLabel)
	}
	if _, err := HerdrSpaceGetByLabel(ctx, root, other.SpaceLabel); err != nil {
		t.Errorf("binding %s must survive; got %v", other.SpaceLabel, err)
	}
	out := buf.String()
	if !strings.Contains(out, "REAPED sandbox=repo/closed") {
		t.Errorf("output missing REAPED line:\n%s", out)
	}
	if strings.Contains(out, "repo/other") {
		t.Errorf("output must not mention the unrelated binding:\n%s", out)
	}
}

func TestHerdrSpacePrune_WorkspaceScoped_DryRunTouchesNothing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:repo/dry", HerdrWorkspaceID: "wDRY",
		SandboxHandle: "repo/dry", SandboxID: "sb-dry", WorktreeManaged: true,
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}
	svc := &fakePruneSvc{sbs: []domain.Sandbox{{Project: "repo", Name: "dry"}}}

	var buf bytes.Buffer
	if err := herdrPluginSpacePrune(ctx, []string{"--workspace", "wDRY"}, &buf, svc, root, ""); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(svc.removed) != 0 {
		t.Errorf("dry-run must not remove; got %v", svc.removed)
	}
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("binding must survive dry-run: %v", err)
	}
	if !strings.Contains(buf.String(), "STALE  sandbox=repo/dry") {
		t.Errorf("dry-run must list the candidate:\n%s", buf.String())
	}
}

func TestHerdrSpacePrune_WorkspaceScoped_UnknownWorkspaceIsNoop(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:repo/keep", HerdrWorkspaceID: "wKEEP",
		SandboxHandle: "repo/keep", SandboxID: "sb-keep", WorktreeManaged: true,
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}
	svc := &fakePruneSvc{sbs: nil}

	var buf bytes.Buffer
	if err := herdrPluginSpacePrune(ctx, []string{"--apply", "--workspace", "wNOPE"}, &buf, svc, root, "/bin/true"); err != nil {
		t.Fatalf("unknown workspace: %v", err)
	}
	if len(svc.removed) != 0 {
		t.Errorf("must not remove anything; got %v", svc.removed)
	}
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("binding must survive: %v", err)
	}
}
