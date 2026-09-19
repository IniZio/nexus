package cli

// Tests for `space-prune --workspace <id>` (the worktree.removed hook path).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

// fakePruneSvc is a herdrSpacePruneLister that also implements
// HerdrSpaceSandboxService, so herdrPluginSpacePrune wires the real
// removeSandbox path to it.
type fakePruneSvc struct {
	fakeSandboxSvc
	sbs []domain.Sandbox
}

func (f *fakePruneSvc) List(_ context.Context) ([]domain.Sandbox, error) { return f.sbs, nil }

// stubWtRemoveVolumes replaces the worktree volume reaper and records the
// handles it was asked to sweep, so prune tests never touch the host's real
// volume store.
func stubWtRemoveVolumes(t *testing.T) *[]string {
	t.Helper()
	var handles []string
	old := herdrWtRemoveVolumesFn
	herdrWtRemoveVolumesFn = func(_ context.Context, _ string, handle string) []string {
		handles = append(handles, handle)
		return []string{herdrDockerDiskVolumeName(handle)}
	}
	t.Cleanup(func() { herdrWtRemoveVolumesFn = old })
	return &handles
}

func TestHerdrSpacePrune_WorkspaceScoped_ReapsOnlyNamedBinding(t *testing.T) {
	stubWtRemoveVolumes(t)
	ctx := context.Background()
	root := t.TempDir()

	closed := HerdrSpaceBinding{
		SpaceLabel: "nexus:repo/closed", HerdrWorkspaceID: "wCLOSED",
		SandboxHandle: "repo/closed", SandboxID: "sb-closed", WorktreeManaged: true,
	}
	other := HerdrSpaceBinding{
		SpaceLabel: "nexus:repo/other", HerdrWorkspaceID: "wOTHER",
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
	swept := stubWtRemoveVolumes(t)
	defer func() {
		if len(*swept) != 0 {
			t.Errorf("dry-run must not remove volumes; swept %v", *swept)
		}
	}()
	ctx := context.Background()
	root := t.TempDir()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus:repo/dry", HerdrWorkspaceID: "wDRY",
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
		SpaceLabel: "nexus:repo/keep", HerdrWorkspaceID: "wKEEP",
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

func TestHerdrSpacePrune_WorkspaceScoped_RemovesWorktreeVolumes(t *testing.T) {
	// Removing a worktree checkout in herdr fires prune --workspace. The
	// sandbox's named volumes (docker/go caches, agentcfg, nested state) are
	// only detached by Service.Remove; with the worktree gone they must be
	// deleted too, whether the VM was still running or already absent.
	// Otherwise every removed worktree leaves ~42 GiB of sparse volumes that
	// disk admission charges in full (17 orphan sets found 2026-09-19).
	swept := stubWtRemoveVolumes(t)
	ctx := context.Background()
	root := t.TempDir()
	running := HerdrSpaceBinding{
		SpaceLabel: "nexus:repo/running", HerdrWorkspaceID: "wGONE",
		SandboxHandle: "repo/running", SandboxID: "sb-running", WorktreeManaged: true,
	}
	absent := HerdrSpaceBinding{
		SpaceLabel: "nexus:repo/absent", HerdrWorkspaceID: "wGONE",
		SandboxHandle: "repo/absent", SandboxID: "sb-absent", WorktreeManaged: true,
	}
	for _, b := range []HerdrSpaceBinding{running, absent} {
		if err := HerdrSpacePut(ctx, root, b); err != nil {
			t.Fatalf("HerdrSpacePut(%s): %v", b.SpaceLabel, err)
		}
	}
	svc := &fakePruneSvc{sbs: []domain.Sandbox{{Project: "repo", Name: "running"}}}

	var buf bytes.Buffer
	if err := herdrPluginSpacePrune(ctx, []string{"--apply", "--workspace", "wGONE"}, &buf, svc, root, "/bin/true"); err != nil {
		t.Fatalf("herdrPluginSpacePrune --workspace: %v", err)
	}
	got := map[string]bool{}
	for _, h := range *swept {
		got[h] = true
	}
	for _, want := range []string{"repo/running", "repo/absent"} {
		if !got[want] {
			t.Errorf("volumes for %s not swept; swept=%v\n%s", want, *swept, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "REMOVED volume="+herdrDockerDiskVolumeName("repo/running")) {
		t.Errorf("output missing REMOVED volume line:\n%s", buf.String())
	}
}
