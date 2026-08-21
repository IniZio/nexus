package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestSvc builds a Service backed by a real FileStore in a temp dir and the
// provided driver.
func newTestSvc(t *testing.T, drv driver.Driver) *Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return New(st, drv, lifecycle.New())
}

// putFakeImage writes a small fake ext4 blob into the cache and returns the
// image metadata so tests can reference its digest/ref.
func putFakeImage(t *testing.T, ctx context.Context, cache *image.Cache) domain.Image {
	t.Helper()
	content := []byte("fake-ext4-rootfs")
	h := sha256.New()
	h.Write(content)
	dig := domain.MustDigest(fmt.Sprintf("sha256:%x", h.Sum(nil)))

	img := domain.Image{
		Digest: dig,
		Ref:    "test-base:20260807",
		Kind:   domain.KindBase,
	}
	if err := cache.Put(ctx, img, bytes.NewReader(content)); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}
	return img
}

// noopProbe is a ProbeFunc that always succeeds (agent is reachable).
var noopProbe ProbeFunc = func(_ context.Context, _ driver.Driver, _ domain.SandboxID) error {
	return nil
}

// errProbe is a ProbeFunc that always fails (agent is unreachable).
func errProbe(err error) ProbeFunc {
	return func(_ context.Context, _ driver.Driver, _ domain.SandboxID) error {
		return err
	}
}

// fakeDriverFactory returns a DriverFactory that always returns the given driver.
func fakeDriverFactory(drv driver.Driver) DriverFactory {
	return func(_ string, _ []ExtraDisk) (driver.Driver, error) {
		return drv, nil
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestCreateAndBoot_ResolvesDigestFromCache verifies that when ImageSpec.Digest
// is set, CreateAndBoot finds the ext4 artifact in the cache, calls Start, and
// records the sandbox as Running.
func TestCreateAndBoot_ResolvesDigestFromCache(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if sb.State != domain.Running {
		t.Errorf("State = %v, want Running", sb.State)
	}
	if sb.Project != "proj" {
		t.Errorf("Project = %q, want proj", sb.Project)
	}
	if sb.Name != "box" {
		t.Errorf("Name = %q, want box", sb.Name)
	}
	if sb.Envelope.ImageDigest != string(img.Digest) {
		t.Errorf("Envelope.ImageDigest = %q, want %q", sb.Envelope.ImageDigest, img.Digest)
	}
	// Verify the record was persisted as Running.
	got, err := svc.store.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.State != domain.Running {
		t.Errorf("persisted State = %v, want Running", got.State)
	}
}

// TestCreateAndBoot_ResolvesRefFromCache verifies that when ImageSpec.Ref is
// set to a human-readable tag, CreateAndBoot scans the cache list and resolves
// the matching image.
func TestCreateAndBoot_ResolvesRefFromCache(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Ref: img.Ref},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.State != domain.Running {
		t.Errorf("State = %v, want Running", sb.State)
	}
	if sb.Envelope.ImageDigest != string(img.Digest) {
		t.Errorf("Envelope.ImageDigest = %q, want %q", sb.Envelope.ImageDigest, img.Digest)
	}
}

// TestCreateAndBoot_ResolvesRootfsPath verifies the --rootfs convenience path:
// the ext4 is used directly without any cache lookup.
func TestCreateAndBoot_ResolvesRootfsPath(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.State != domain.Running {
		t.Errorf("State = %v, want Running", sb.State)
	}
	// No image digest when using rootfs path directly.
	if sb.Envelope.ImageDigest != "" {
		t.Errorf("Envelope.ImageDigest = %q, want empty for --rootfs", sb.Envelope.ImageDigest)
	}
}

// TestCreateAndBoot_ProbeFailureCleansUp verifies that when the reachability
// probe fails, the VM is stopped, the record is deleted, and the error wraps
// ErrAgentUnreachable. No orphan record is left in the store.
func TestCreateAndBoot_ProbeFailureCleansUp(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	simErr := errors.New("vsock: connection refused")
	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), errProbe(simErr),
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
		},
	)
	if err == nil {
		t.Fatal("expected error from probe failure, got nil")
	}
	if !errors.Is(err, ErrAgentUnreachable) {
		t.Errorf("error does not wrap ErrAgentUnreachable: %v", err)
	}

	// Store must be empty — no orphan record.
	all, listErr := svc.List(ctx)
	if listErr != nil {
		t.Fatalf("svc.List: %v", listErr)
	}
	if len(all) != 0 {
		t.Errorf("expected empty store after probe failure, got %d records", len(all))
	}
}

// TestCreateAndBoot_DriverStartFailureCleansUp verifies that when driver.Start
// fails, the record is deleted and the error propagates without wrapping
// ErrAgentUnreachable.
func TestCreateAndBoot_DriverStartFailureCleansUp(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	simErr := errors.New("VMM: out of memory")
	fd.SetStartError(simErr)
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
		},
	)
	if err == nil {
		t.Fatal("expected error from Start failure, got nil")
	}
	if errors.Is(err, ErrAgentUnreachable) {
		t.Error("error should not wrap ErrAgentUnreachable for a Start failure")
	}

	// Store must be empty — no orphan record.
	all, listErr := svc.List(ctx)
	if listErr != nil {
		t.Fatalf("svc.List: %v", listErr)
	}
	if len(all) != 0 {
		t.Errorf("expected empty store after Start failure, got %d records", len(all))
	}
}

// TestCreateAndBoot_NilSpecErrors verifies that calling CreateAndBoot with an
// empty ImageSpec returns a clear error and writes no record.
func TestCreateAndBoot_NilSpecErrors(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{}, // all empty
			CacheRoot: cacheRoot,
		},
	)
	if err == nil {
		t.Fatal("expected error for empty ImageSpec, got nil")
	}
}

// TestCreateAndBoot_ExtraDisksReachFactory verifies the ExtraDisks threading
// seam: ExtraDisk values placed in CreateAndBootOptions.ExtraDisks are
// forwarded verbatim to the DriverFactory as the extraDisks argument.
// The test uses a capturing factory so no real VM or vsock is needed.
func TestCreateAndBoot_ExtraDisksReachFactory(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	want := []ExtraDisk{
		{Path: "/mnt/scratch/vdb.raw"},
		{Path: "/mnt/cache/vdc.raw"},
	}

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = extraDisks
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, capturingFactory, noopProbe,
		"proj", "extra-disks",
		CreateAndBootOptions{
			Image:      ImageSpec{Digest: string(img.Digest)},
			CacheRoot:  cacheRoot,
			ExtraDisks: want,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if len(capturedDisks) != len(want) {
		t.Fatalf("factory received %d extra disks, want %d", len(capturedDisks), len(want))
	}
	for i, d := range capturedDisks {
		if d.Path != want[i].Path {
			t.Errorf("ExtraDisk[%d].Path = %q, want %q", i, d.Path, want[i].Path)
		}
	}
}

// stubCapturer returns a WorkspaceCapturer stub that creates an empty file at
// outExt4 (so the deferred cleanup has something to remove) and records the
// arguments it was called with. The returned pointer fields are populated
// after CreateAndBoot returns.
func stubCapturer(calledSrcDir, calledOutExt4 *string, calledMaxBytes *int64) func(ctx context.Context, srcDir, outExt4 string, maxBytes int64) error {
	return func(_ context.Context, srcDir, outExt4 string, maxBytes int64) error {
		if calledSrcDir != nil {
			*calledSrcDir = srcDir
		}
		if calledMaxBytes != nil {
			*calledMaxBytes = maxBytes
		}
		if calledOutExt4 != nil {
			*calledOutExt4 = outExt4
		}
		// Create an empty placeholder so the deferred os.Remove succeeds.
		f, err := os.Create(outExt4)
		if err != nil {
			return err
		}
		return f.Close()
	}
}

// TestCreateAndBoot_WorkspaceReachesFactory verifies the Workspace threading
// seam: when Workspace is set, the captured disk path is appended to the
// ExtraDisks slice forwarded to the DriverFactory. The test uses an injected
// WorkspaceCapturer stub so no mke2fs binary is required.
func TestCreateAndBoot_WorkspaceReachesFactory(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	diskDir := t.TempDir()

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = extraDisks
		return fake.New(), nil
	})

	var calledOutExt4 string
	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, capturingFactory, noopProbe,
		"proj", "ws-seam",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
			Workspace: &WorkspaceSpec{
				SourcePath: "/host/repo",
				GuestPath:  "/workspace/repo",
			},
			WorkspaceCapturer: stubCapturer(nil, &calledOutExt4, nil),
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// The factory must have received exactly one extra disk (the workspace).
	if len(capturedDisks) != 1 {
		t.Fatalf("factory received %d extra disks, want 1", len(capturedDisks))
	}
	// The disk path forwarded to the factory must match the one the capturer wrote.
	if capturedDisks[0].Path != calledOutExt4 {
		t.Errorf("factory ExtraDisk[0].Path = %q, capturer wrote to %q", capturedDisks[0].Path, calledOutExt4)
	}
}

// TestCreateAndBoot_NoWorkspace_LegacyBehaviour verifies that a nil Workspace
// field leaves ExtraDisks untouched: the DriverFactory receives an empty slice
// (not a workspace disk appended behind the caller's back).
func TestCreateAndBoot_NoWorkspace_LegacyBehaviour(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = extraDisks
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, capturingFactory, noopProbe,
		"proj", "no-ws",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			// Workspace intentionally nil.
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if len(capturedDisks) != 0 {
		t.Errorf("factory received %d extra disks with nil Workspace, want 0", len(capturedDisks))
	}
}

// TestCreateAndBoot_WorkspaceZeroCaptureMaxPassedThrough verifies that a zero
// CaptureMaxBytes in WorkspaceSpec is forwarded as-is to the capturer. Zero
// means "auto" (free-space-derived guard); the capturer, not the service layer,
// resolves the actual limit.
func TestCreateAndBoot_WorkspaceZeroCaptureMaxPassedThrough(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	diskDir := t.TempDir()

	var calledMaxBytes int64
	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "ws-threshold",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
			Workspace: &WorkspaceSpec{
				SourcePath:      "/host/repo",
				GuestPath:       "/workspace/repo",
				CaptureMaxBytes: 0, // zero → auto; must be forwarded as 0
			},
			WorkspaceCapturer: stubCapturer(nil, nil, &calledMaxBytes),
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if calledMaxBytes != 0 {
		t.Errorf("capturer maxBytes = %d, want 0 (auto — service must not substitute a constant)", calledMaxBytes)
	}
}

// TestCreateAndBoot_AllowedRepoReachesEnvelope verifies end-to-end that
// CreateAndBootOptions.AllowedRepo is stored in the sandbox Envelope (D-PD-36).
// The test drives the REAL CreateAndBoot function against a fake driver.
//
// Mutation evidence: remove the `AllowedRepo: opts.AllowedRepo` assignment
// in the Envelope block of CreateAndBoot → this test fails with
// Envelope.AllowedRepo = "" (want "acme/myrepo"). Restore → passes.
func TestCreateAndBoot_AllowedRepoReachesEnvelope(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "d36box",
		CreateAndBootOptions{
			Image:       ImageSpec{Digest: string(img.Digest)},
			CacheRoot:   cacheRoot,
			AllowedRepo: "acme/myrepo",
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.Envelope.AllowedRepo != "acme/myrepo" {
		t.Errorf("Envelope.AllowedRepo = %q, want %q", sb.Envelope.AllowedRepo, "acme/myrepo")
	}
}

// TestCreateAndBoot_GitHubSecretWithoutRepo_Refused verifies the D-PD-36
// service-layer invariant: a GitHub secret bind without AllowedRepo is refused
// by CreateAndBoot directly, regardless of caller (CLI, MCP, orca, herdr).
//
// Mutation evidence: remove the ErrUnboundGitHubSecret guard in CreateAndBoot
// → this test fails (got nil error, want ErrUnboundGitHubSecret). Restore → passes.
func TestCreateAndBoot_GitHubSecretWithoutRepo_Refused(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "gh-no-repo",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			// GitHub secret bind, AllowedRepo deliberately left empty.
			Secrets: []SecretBind{{Env: BuiltinGitHubEnv, Hosts: GitHubSecretHosts, Token: "ghp_testtoken"}},
			// AllowedRepo: "",  // omitted — triggers the guard
		},
	)
	if !errors.Is(err, ErrUnboundGitHubSecret) {
		t.Fatalf("CreateAndBoot: got %v, want ErrUnboundGitHubSecret", err)
	}
}

// TestCreateAndBoot_GitHubSecretWithRepo_Allowed verifies that the D-PD-36
// guard does NOT fire when AllowedRepo is set — the allowed combination succeeds.
func TestCreateAndBoot_GitHubSecretWithRepo_Allowed(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "gh-with-repo",
		CreateAndBootOptions{
			Image:       ImageSpec{Digest: string(img.Digest)},
			CacheRoot:   cacheRoot,
			Secrets:     []SecretBind{{Env: BuiltinGitHubEnv, Hosts: GitHubSecretHosts, Token: "ghp_testtoken"}},
			AllowedRepo: "owner/repo", // D-PD-36: guard must NOT fire
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.Envelope.AllowedRepo != "owner/repo" {
		t.Errorf("Envelope.AllowedRepo = %q, want %q", sb.Envelope.AllowedRepo, "owner/repo")
	}
}

// TestCreateAndBoot_DShl05_AgentGitHubGuards covers the D-SHL-05 invariant
// change: the blanket agent-GitHub ban is replaced by the unconditional
// AllowedRepo requirement (ErrUnboundGitHubSecret) shared with all callers.
//
// Table:
//   agent + GitHub + AllowedRepo  → ACCEPTED  (D-SHL-05)
//   agent + GitHub + no AllowedRepo → REFUSED  (ErrUnboundGitHubSecret, D-PD-36)
//   non-agent + GitHub + no AllowedRepo → REFUSED (unchanged)
//   agent + mixed-host secret     → REFUSED   (ErrMixedGitHubSecret, unchanged)
//
// Mutation evidence for each case is documented inline; run with -v to see names.
func TestCreateAndBoot_DShl05_AgentGitHubGuards(t *testing.T) {
	type tc struct {
		name        string
		useAgent    bool   // sets UseAgentSeed=true
		agentName   bool   // sets AgentProfile=ClaudeCodeProfile
		githubBind  bool   // includes a GitHub SecretBind
		mixedBind   bool   // includes a mixed-host SecretBind
		allowedRepo string // empty = omitted
		wantErr     error  // nil = expect success
	}
	cases := []tc{
		{
			name:        "agent_seed+GitHub+repo=accepted",
			useAgent:    true,
			githubBind:  true,
			allowedRepo: "owner/repo",
			wantErr:     nil,
		},
		{
			name:       "agent_seed+GitHub+no_repo=refused",
			useAgent:   true,
			githubBind: true,
			wantErr:    ErrUnboundGitHubSecret,
		},
		{
			name:       "non_agent+GitHub+no_repo=refused",
			githubBind: true,
			wantErr:    ErrUnboundGitHubSecret,
		},
		{
			name:      "agent_profile+GitHub+repo=accepted",
			agentName: true,
			githubBind: true,
			allowedRepo: "owner/repo",
			wantErr:   nil,
		},
		{
			name:      "agent_profile+GitHub+no_repo=refused",
			agentName: true,
			githubBind: true,
			wantErr:   ErrUnboundGitHubSecret,
		},
		{
			name:      "agent_seed+mixed_host=refused",
			useAgent:  true,
			mixedBind: true,
			wantErr:   ErrMixedGitHubSecret,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			cacheRoot := t.TempDir()
			cache, err := image.NewCache(cacheRoot)
			if err != nil {
				t.Fatalf("NewCache: %v", err)
			}
			img := putFakeImage(t, ctx, cache)
			fd := fake.New()
			svc := newTestSvc(t, fd)

			var secrets []SecretBind
			if c.githubBind {
				secrets = append(secrets, SecretBind{
					Env:   BuiltinGitHubEnv,
					Hosts: append([]string(nil), GitHubSecretHosts...),
					Token: "ghp_test",
				})
			}
			if c.mixedBind {
				secrets = append(secrets, SecretBind{
					Env:   "MIXED_TOKEN",
					Hosts: []string{"github.com", "registry.example.com"},
					Token: "tok",
				})
			}

			opts := CreateAndBootOptions{
				Image:        ImageSpec{Digest: string(img.Digest)},
				CacheRoot:    cacheRoot,
				UseAgentSeed: c.useAgent,
				Secrets:      secrets,
				AllowedRepo:  c.allowedRepo,
			}
			if c.agentName {
				opts.AgentProfile = cred.ClaudeCodeProfile
			}

			_, gotErr := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
				"proj", c.name, opts)

			if c.wantErr == nil {
				if gotErr != nil {
					t.Fatalf("CreateAndBoot: got error %v, want success", gotErr)
				}
			} else {
				if !errors.Is(gotErr, c.wantErr) {
					t.Fatalf("CreateAndBoot: got %v, want %v", gotErr, c.wantErr)
				}
			}
		})
	}
}

// TestCreateAndBoot_LiveMounts_FlowsToRecord verifies that LiveMounts set in
// CreateAndBootOptions are persisted on the sandbox record (D-PD-53).
//
// The DriverFactory is a closure (not the real cloudhypervisor driver), so this
// test verifies the service layer — that opts.LiveMounts reaches sb.LiveMounts
// — without exercising virtiofsd or the kernel cmdline. The CLI-layer
// liveMountsToGuestMounts + VirtiofsTag agreement is covered in the cli package
// (TestLiveMountsToGuestMounts_TagAgreement).
func TestCreateAndBoot_LiveMounts_FlowsToRecord(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	mounts := []domain.LiveMount{
		{HostPath: "/host/a", GuestPath: "/guest/a", ReadOnly: false},
		{HostPath: "/host/b", GuestPath: "/guest/b", ReadOnly: true},
	}

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "live-mounts-test",
		CreateAndBootOptions{
			Image:      ImageSpec{Digest: string(img.Digest)},
			CacheRoot:  cacheRoot,
			LiveMounts: mounts,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// Verify all three acceptance criteria at once:
	// 1. LiveMounts length matches.
	// 2. HostPath, GuestPath, ReadOnly propagate correctly for each entry.
	if len(sb.LiveMounts) != len(mounts) {
		t.Fatalf("sb.LiveMounts len = %d, want %d", len(sb.LiveMounts), len(mounts))
	}
	for i, want := range mounts {
		got := sb.LiveMounts[i]
		if got.HostPath != want.HostPath {
			t.Errorf("LiveMounts[%d].HostPath = %q, want %q", i, got.HostPath, want.HostPath)
		}
		if got.GuestPath != want.GuestPath {
			t.Errorf("LiveMounts[%d].GuestPath = %q, want %q", i, got.GuestPath, want.GuestPath)
		}
		if got.ReadOnly != want.ReadOnly {
			t.Errorf("LiveMounts[%d].ReadOnly = %v, want %v", i, got.ReadOnly, want.ReadOnly)
		}
	}
}

// TestCreateAndBoot_LiveMounts_ReadOnly_EndToEnd proves that :ro propagates
// through the domain record correctly — ReadOnly=true on the second mount and
// ReadOnly=false on the first are both preserved.
func TestCreateAndBoot_LiveMounts_ReadOnly_EndToEnd(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "ro-live-mount",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			LiveMounts: []domain.LiveMount{
				{HostPath: "/host/rw", GuestPath: "/guest/rw", ReadOnly: false},
				{HostPath: "/host/ro", GuestPath: "/guest/ro", ReadOnly: true},
			},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.LiveMounts[0].ReadOnly {
		t.Error("LiveMounts[0].ReadOnly: got true, want false")
	}
	if !sb.LiveMounts[1].ReadOnly {
		t.Error("LiveMounts[1].ReadOnly: got false, want true")
	}
}
