package builder_test

// TestSizeVerifiedFSLive proves that the sizeVerifiedFS guard on the build-context
// local mount surfaces truncated file reads as hard errors on Solve, rather than
// silently producing a poisoned image (D-8).
//
// Four sub-proofs plus three mutations, run sequentially:
//
//	Part A:        a cutFS (Open returns EOF after 4 MiB for a 64 MiB file) wrapped
//	               with NewSizeVerifiedFS causes bk.Solve to return a non-nil error.
//
//	Part B:        builder.Build (which wraps the context FS with newSizeVerifiedFS
//	               internally) succeeds on a real FS, and debugfs confirms the 64 MiB
//	               file lands at its full size inside the ext4 artifact.
//
//	Part C:        the Open-failure guard fires — inner Open failure causes Solve to
//	               fail fast rather than silently writing a zero-byte file.
//
//	Part C mutation: raw failOpenFS without the wrapper — Solve may succeed and
//	               write a zero-byte file, proving the guard is necessary.
//
//	Part D:        sizeVerifiedSet wrapping the nexus3agent mount fails fast when
//	               the agent read is truncated.
//
//	Mutation (A):  the same cutFS used in Part A, but WITHOUT the sizeVerifiedFS
//	               wrapper, lets bk.Solve succeed — delivering a silently truncated
//	               file.  The unpacked bigfile.dat size (< 64 MiB) proves the guard
//	               is necessary.

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	gofs "io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"github.com/tonistiigi/fsutil"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/image"
)

// w34LiveEndpoint returns the buildkitd address and true if buildkitd is
// reachable. It checks BUILDKIT_HOST first, then the default socket path.
// Inlined here so this file compiles without the integration build tag.
func w34LiveEndpoint() (string, bool) {
	if h := os.Getenv("BUILDKIT_HOST"); h != "" {
		return h, true
	}
	const defaultSock = "/run/buildkit/buildkitd.sock"
	if _, err := os.Stat(defaultSock); err == nil {
		return "unix://" + defaultSock, true
	}
	return "", false
}

// w34RepoRoot returns the module root by parsing go env GOMOD.
func w34RepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Skipf("skipping: go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Skip("skipping: not in a Go module")
	}
	return filepath.Dir(mod)
}

// w34BuildNexus3Agent compiles cmd/nexus3-agent as a static Linux/amd64 binary
// and returns its path in a temp dir cleaned up when t ends.
func w34BuildNexus3Agent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nexus3-agent")
	cmd := exec.Command("go", "build", "-o", bin,
		"github.com/IniZio/nexus3/cmd/nexus3-agent")
	cmd.Dir = w34RepoRoot(t)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build nexus3-agent: %s\n%v", out, err)
	}
	return bin
}

// failOpenFS wraps an fsutil.FS and returns an error from Open for any path
// that contains failFor (matched via strings.Contains). Walk is delegated
// unchanged so that sizeVerifiedFS sees the full declared size, then receives
// an Open error — the exact condition that causes fsutil to emit a zero-byte
// file silently (send.go:136-147 skips the copy on Open failure).
type failOpenFS struct {
	inner   fsutil.FS
	failFor string // substring matched against path
}

func (f *failOpenFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	return f.inner.Walk(ctx, target, fn)
}

func (f *failOpenFS) Open(path string) (io.ReadCloser, error) {
	if strings.Contains(path, f.failFor) {
		return nil, fmt.Errorf("injected open failure for %q", path)
	}
	return f.inner.Open(path)
}

// cutFS wraps an fsutil.FS and truncates every Open read at cutAt bytes,
// simulating a source file that is shorter than its declared stat size.
// Walk is delegated unchanged so that sizeVerifiedFS sees the full declared
// size on entry but gets a short reader when it calls Open.
type cutFS struct {
	inner fsutil.FS
	cutAt int64
}

func (c *cutFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	return c.inner.Walk(ctx, target, fn)
}

func (c *cutFS) Open(path string) (io.ReadCloser, error) {
	rc, err := c.inner.Open(path)
	if err != nil {
		return nil, err
	}
	return &cutReader{ReadCloser: rc, rem: c.cutAt}, nil
}

// cutReader returns io.EOF after rem bytes have been delivered.
type cutReader struct {
	io.ReadCloser
	rem int64
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.rem <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.rem {
		p = p[:r.rem]
	}
	n, err := r.ReadCloser.Read(p)
	r.rem -= int64(n)
	if r.rem <= 0 && err == nil {
		err = io.EOF
	}
	return n, err
}

func TestSizeVerifiedFSLive(t *testing.T) {
	buildkitHost, ok := w34LiveEndpoint()
	if !ok {
		t.Skip("no buildkitd available: set BUILDKIT_HOST or start buildkitd at /run/buildkit/buildkitd.sock")
	}
	if !builder.Mke2fsAvailable() {
		t.Skip("skipping: mke2fs not in PATH")
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	})

	// Build a 64 MiB file with a unique nanosecond-timestamp prefix so buildkitd
	// cannot return a content-store cache hit from a previous invocation.
	const fileSize = 64 << 20
	bigfile := make([]byte, fileSize)
	binary.BigEndian.PutUint64(bigfile[:8], uint64(time.Now().UnixNano()))

	// Shared context dir for Part A + mutation.  Only bigfile.dat is present;
	// the Dockerfile is kept in a separate dfDir so it is not included in the
	// build context that fsutil.NewFS scans.
	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "bigfile.dat"), bigfile, 0o644); err != nil {
		t.Fatalf("write bigfile.dat: %v", err)
	}

	ctxFS, err := fsutil.NewFS(ctxDir)
	if err != nil {
		t.Fatalf("fsutil.NewFS(ctxDir): %v", err)
	}
	cut := &cutFS{inner: ctxFS, cutAt: 4 << 20}

	// solveWithMounts calls bk.Solve with the given local mounts and a tar
	// exporter that unpacks into outDir. bkCtx gates the bkclient.New call;
	// solveCtx is passed to bk.Solve so callers can cancel it independently
	// (e.g. via a context.WithCancelCause wired to a sizeVerifiedFS).
	solveWithMounts := func(
		bkCtx context.Context,
		solveCtx context.Context,
		host string,
		mounts map[string]fsutil.FS,
		dfDir string,
		outDir string,
	) error {
		dfFS, fsErr := fsutil.NewFS(dfDir)
		if fsErr != nil {
			return fmt.Errorf("fsutil.NewFS(dfDir): %w", fsErr)
		}
		mounts["dockerfile"] = dfFS
		return builder.ExportAndUnpack(bkCtx, outDir, func(egCtx context.Context, pw io.WriteCloser) error {
			bk, bkErr := bkclient.New(egCtx, host)
			if bkErr != nil {
				return fmt.Errorf("bkclient.New: %w", bkErr)
			}
			defer bk.Close()
			_, solveErr := bk.Solve(solveCtx, nil, bkclient.SolveOpt{
				LocalMounts: mounts,
				Frontend:    "dockerfile.v0",
				FrontendAttrs: map[string]string{
					"filename": "Dockerfile",
				},
				Exports: []bkclient.ExportEntry{{
					Type: bkclient.ExporterTar,
					Output: func(_ map[string]string) (io.WriteCloser, error) {
						return pw, nil
					},
				}},
			}, nil)
			return solveErr
		})
	}

	// writeDFDir writes a Dockerfile with a unique probe RUN into a new temp dir.
	writeDFDir := func(probe string) string {
		dir := t.TempDir()
		df := "FROM alpine:latest\n" +
			fmt.Sprintf("RUN echo %d > /.nexus3-w34-%s\n", time.Now().UnixNano(), probe) +
			"COPY bigfile.dat /bigfile.dat\n"
		t.Logf("Dockerfile (%s):\n%s", probe, df)
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
			t.Fatalf("write Dockerfile (%s): %v", probe, err)
		}
		return dir
	}

	// ── Part A: sizeVerifiedFS(cutFS) → Solve MUST fail fast with our error ──
	t.Log("Part A: sizeVerifiedFS + cutFS — Solve must fail within seconds with a descriptive error")
	{
		outDirA := t.TempDir()
		ctxA, cancelA := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelA()

		// solveCtx is what we pass to bk.Solve; cancelCause fires on first
		// size violation so Solve tears down immediately rather than at deadline.
		solveCtxA, cancelCauseA := context.WithCancelCause(ctxA)
		defer cancelCauseA(nil)
		sfsA := builder.NewSizeVerifiedFS(cut, cancelCauseA)

		startA := time.Now()
		errA := solveWithMounts(ctxA, solveCtxA, buildkitHost, map[string]fsutil.FS{
			"context": sfsA,
		}, writeDFDir("probe-A"), outDirA)
		elapsedA := time.Since(startA)

		// Prefer our descriptive error over the context-cancellation error.
		if fsErrA := sfsA.Err(); fsErrA != nil {
			errA = fsErrA
		}

		if errA == nil {
			t.Fatal("Part A: expected non-nil error from sizeVerifiedFS truncation guard, got nil")
		}
		t.Logf("Part A: elapsed=%s error: %v", elapsedA.Round(time.Second), errA)

		msg := errA.Error()
		if !strings.Contains(msg, "bigfile.dat") {
			t.Errorf("Part A: error does not mention file path: %s", msg)
		}
		if !strings.Contains(msg, "4194304") {
			t.Errorf("Part A: error does not mention got-bytes 4194304: %s", msg)
		}
		if !strings.Contains(msg, "67108864") {
			t.Errorf("Part A: error does not mention expected-bytes 67108864: %s", msg)
		}
		if elapsedA > 2*time.Minute {
			t.Errorf("Part A: Solve took %s — cancel-cause not propagating fast enough", elapsedA.Round(time.Second))
		}

		// Guard must prevent any output from landing in outDir.
		if fi, statErr := os.Stat(filepath.Join(outDirA, "bigfile.dat")); statErr == nil {
			t.Errorf("Part A: bigfile.dat unexpectedly present in outDir (size=%d) — guard did not stop the build", fi.Size())
		}
	}

	// ── Part B: builder.Build (real FS) → ext4 with full 64 MiB file ─────────
	t.Log("Part B: normal builder.Build — must succeed and produce 64 MiB file in ext4")
	{
		agentBin := w34BuildNexus3Agent(t)

		// Fresh bigfile with a different timestamp prefix so Part B's COPY layer
		// hash is independent of Part A/mutation and immune to their cache entries.
		bigfileB := make([]byte, fileSize)
		binary.BigEndian.PutUint64(bigfileB[:8], uint64(time.Now().UnixNano()))

		workspaceB := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspaceB, "bigfile.dat"), bigfileB, 0o644); err != nil {
			t.Fatalf("Part B: write bigfile.dat: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(workspaceB, ".nexus"), 0o755); err != nil {
			t.Fatalf("Part B: mkdir .nexus: %v", err)
		}
		cfB := "FROM alpine:latest\n" +
			fmt.Sprintf("RUN echo %d > /.nexus3-w34-probe-B\n", time.Now().UnixNano()) +
			"COPY bigfile.dat /bigfile.dat\n"
		t.Logf("Part B Containerfile:\n%s", cfB)
		if err := os.WriteFile(filepath.Join(workspaceB, ".nexus", "Containerfile"), []byte(cfB), 0o644); err != nil {
			t.Fatalf("Part B: write Containerfile: %v", err)
		}

		// Fixed (persistent) cache dir so the ext4 artifact survives test teardown
		// and can be inspected with debugfs externally.
		cacheDirB := "/tmp/nexus3-w34-sizedfs-live-cache"
		if err := os.MkdirAll(cacheDirB, 0o755); err != nil {
			t.Fatalf("Part B: mkdir cache: %v", err)
		}
		cacheB, err := image.NewCache(cacheDirB)
		if err != nil {
			t.Fatalf("Part B: image.NewCache: %v", err)
		}

		b, err := builder.New(builder.Config{
			BuildkitdAddr:   buildkitHost,
			AgentBinaryPath: agentBin,
		}, cacheB)
		if err != nil {
			t.Fatalf("Part B: builder.New (connect failed): %v", err)
		}

		ctxB, cancelB := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelB()

		startB := time.Now()
		img, err := b.Build(ctxB, builder.BuildRequest{
			BaseRef:      "alpine:latest",
			WorkspaceDir: workspaceB,
			Ref:          "w34-sizedfs-live:alpine",
		})
		if err != nil {
			t.Fatalf("Part B: Builder.Build (elapsed %s): %v", time.Since(startB).Round(time.Second), err)
		}
		t.Logf("Part B: build completed: elapsed=%s digest=%s size=%d",
			time.Since(startB).Round(time.Second), img.Digest, img.Size)

		// Locate the ext4 artifact in the persistent cache dir.
		var ext4PathB string
		_ = filepath.Walk(cacheDirB, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return err
			}
			if filepath.Base(p) == "artifact" {
				ext4PathB = p
			}
			return nil
		})
		if ext4PathB == "" {
			t.Fatal("Part B: could not locate ext4 artifact in cache dir")
		}
		t.Logf("Part B: ext4 artifact: %s", ext4PathB)

		// debugfs: stat /bigfile.dat — must report the full 64 MiB (67108864 bytes).
		dbOut, dbErr := exec.Command("debugfs", "-R", "stat /bigfile.dat", ext4PathB).CombinedOutput()
		if dbErr != nil {
			t.Logf("Part B: debugfs unavailable or failed: %v\n%s", dbErr, dbOut)
		} else {
			t.Logf("Part B: debugfs stat /bigfile.dat:\n%s", dbOut)
			wantSize := fmt.Sprintf("%d", int64(fileSize))
			if !strings.Contains(string(dbOut), wantSize) {
				t.Errorf("Part B: debugfs output does not contain expected size %s bytes:\n%s", wantSize, dbOut)
			}
		}
	}

	// ── Part C: inner Open fails for the 64 MiB file → Solve MUST fail fast ────
	// This proves the zero-byte-file guard: fsutil's sendFile skips the copy
	// and emits a terminating PACKET_DATA on Open failure, writing a zero-byte
	// file silently unless we cancel the Solve context from Open.
	t.Log("Part C: Open-failure guard — inner Open fails for bigfile.dat; Solve must fail within seconds")
	{
		// failOpenFS delegates Walk to ctxFS but returns an error from Open,
		// simulating a host file that disappears between Walk and Open.
		ctxFSC, ctxErrC := fsutil.NewFS(ctxDir)
		if ctxErrC != nil {
			t.Fatalf("Part C: fsutil.NewFS: %v", ctxErrC)
		}
		failFS := &failOpenFS{inner: ctxFSC, failFor: "bigfile.dat"}

		outDirC := t.TempDir()
		ctxC, cancelC := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelC()

		solveCtxC, cancelCauseC := context.WithCancelCause(ctxC)
		defer cancelCauseC(nil)
		sfsC := builder.NewSizeVerifiedFS(failFS, cancelCauseC)

		startC := time.Now()
		errC := solveWithMounts(ctxC, solveCtxC, buildkitHost, map[string]fsutil.FS{
			"context": sfsC,
		}, writeDFDir("probe-C"), outDirC)
		elapsedC := time.Since(startC)

		if fsErrC := sfsC.Err(); fsErrC != nil {
			errC = fsErrC
		}

		if errC == nil {
			t.Fatal("Part C: expected non-nil error from Open-failure guard, got nil")
		}
		t.Logf("Part C: elapsed=%s error: %v", elapsedC.Round(time.Second), errC)

		if !strings.Contains(errC.Error(), "bigfile.dat") {
			t.Errorf("Part C: error does not mention file path: %s", errC)
		}
		if elapsedC > 2*time.Minute {
			t.Errorf("Part C: Solve took %s — cancel-cause not propagating fast enough", elapsedC.Round(time.Second))
		}
		if _, statErr := os.Stat(filepath.Join(outDirC, "bigfile.dat")); statErr == nil {
			t.Error("Part C: bigfile.dat unexpectedly present in outDir — guard did not stop the build")
		}
	}

	// ── Part C mutation: raw failOpenFS (no wrapper) → Solve SUCCEEDS, Size: 0 ─
	t.Log("Part C mutation: raw failOpenFS without wrapper — Solve must succeed with a zero-byte file")
	{
		ctxFSCM, ctxErrCM := fsutil.NewFS(ctxDir)
		if ctxErrCM != nil {
			t.Fatalf("Part C mutation: fsutil.NewFS: %v", ctxErrCM)
		}
		failFSM := &failOpenFS{inner: ctxFSCM, failFor: "bigfile.dat"}

		outDirCM := t.TempDir()
		ctxCM, cancelCM := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelCM()

		errCM := solveWithMounts(ctxCM, ctxCM, buildkitHost, map[string]fsutil.FS{
			"context": failFSM,
		}, writeDFDir("probe-CM"), outDirCM)

		if errCM != nil {
			t.Logf("Part C mutation: Solve failed (buildkitd may now validate open errors): %v", errCM)
			t.Log("NOTE: Part C mutation inconclusive — buildkitd rejected the open error")
		} else {
			t.Log("MUTATION Part C: raw failOpenFS — Solve succeeded (expected)")
			// bigfile.dat must be zero bytes in the unpacked output.
			fi, statErr := os.Stat(filepath.Join(outDirCM, "bigfile.dat"))
			if statErr != nil {
				t.Logf("Part C mutation: bigfile.dat not in unpacked outDir (buildkitd stored zero): %v", statErr)
			} else {
				t.Logf("Part C mutation: bigfile.dat size=%d bytes — zero-byte fault reproduced", fi.Size())
				if fi.Size() != 0 {
					t.Errorf("Part C mutation: expected 0-byte file, got %d bytes", fi.Size())
				}
			}
		}
	}

	// ── Mutation: raw Solve with cutFS (no wrapper) → SUCCEEDS, file truncated ─
	// This sub-proof demonstrates why the guard is necessary: without it, buildkitd
	// silently accepts the 4 MiB short-read and the build appears to succeed.
	t.Log("Mutation: raw Solve with cutFS — no sizeVerifiedFS wrapper")
	{
		outDirMut := t.TempDir()
		ctxMut, cancelMut := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelMut()

		// cutFS passed directly — no wrapper and no cancel-cause.
		errMut := solveWithMounts(ctxMut, ctxMut, buildkitHost, map[string]fsutil.FS{
			"context": cut,
		}, writeDFDir("probe-MUT"), outDirMut)

		if errMut != nil {
			// buildkitd validated the size itself — the guard is redundant for this
			// buildkitd version, but the test is still meaningful as a regression
			// guard for versions that do not.
			t.Logf("Mutation: Solve failed (buildkitd may now validate sizes): %v", errMut)
			t.Log("NOTE: mutation proof inconclusive — buildkitd rejected the short read before sizeVerifiedFS could")
			return
		}

		t.Log("MUTATION: bypass guard — Solve with cut FS succeeded (expected)")

		// The unpacked bigfile.dat must be shorter than the declared 64 MiB.
		fi, statErr := os.Stat(filepath.Join(outDirMut, "bigfile.dat"))
		if statErr != nil {
			t.Logf("Mutation: bigfile.dat not found in outDir (buildkitd may have stored zero-length file): %v", statErr)
			return
		}
		t.Logf("Mutation: bigfile.dat in unpacked output: %d bytes (%.1f MiB) — declared size was %d bytes (%.1f MiB)",
			fi.Size(), float64(fi.Size())/(1<<20),
			int64(fileSize), float64(fileSize)/(1<<20))
		if fi.Size() >= int64(fileSize) {
			t.Errorf("Mutation: bigfile.dat size %d >= declared %d — guard was effective even without the wrapper (unexpected)",
				fi.Size(), int64(fileSize))
		}
	}

	// ── Part D: sfsSet wrapping nexus3agent → Solve MUST fail fast ───────────
	// Proves that the nexus3agent mount (which carries the ~36 MiB agent binary,
	// the exact artifact class that triggered the production 32 MiB truncation)
	// is now guarded: a 4 MiB cut triggers the same fail-fast as Part A.
	t.Log("Part D: sizeVerifiedSet wrapping nexus3agent — Solve must fail fast, error names agent path")
	{
		agentBinD := w34BuildNexus3Agent(t)
		agentBytesD, agentReadErr := os.ReadFile(agentBinD)
		if agentReadErr != nil {
			t.Fatalf("Part D: read agent binary: %v", agentReadErr)
		}
		t.Logf("Part D: agent binary size: %d bytes (%.1f MiB)", len(agentBytesD), float64(len(agentBytesD))/(1<<20))

		// Agent dir contains only _nexus3-agent (the binary), matching the
		// production layout that Solve uses for the nexus3agent named context.
		agentDirD := t.TempDir()
		if err := os.WriteFile(filepath.Join(agentDirD, "_nexus3-agent"), agentBytesD, 0o755); err != nil {
			t.Fatalf("Part D: write agent: %v", err)
		}
		agentFSD_real, agentFSErrD := fsutil.NewFS(agentDirD)
		if agentFSErrD != nil {
			t.Fatalf("Part D: fsutil.NewFS(agentDir): %v", agentFSErrD)
		}
		agentCutD := &cutFS{inner: agentFSD_real, cutAt: 4 << 20}

		// Minimal build context (no workspace files — agent is in nexus3agent context).
		ctxDirD := t.TempDir()
		ctxFSD, ctxFSErrD := fsutil.NewFS(ctxDirD)
		if ctxFSErrD != nil {
			t.Fatalf("Part D: fsutil.NewFS(ctxDir): %v", ctxFSErrD)
		}

		// Dockerfile COPYs the agent from the nexus3agent named context.
		dfDirD := t.TempDir()
		dfD := "FROM alpine:latest\n" +
			fmt.Sprintf("RUN echo %d > /.nexus3-w39-probe-D\n", time.Now().UnixNano()) +
			"COPY --chmod=0755 --from=nexus3agent _nexus3-agent /sbin/nexus3-agent\n"
		t.Logf("Part D Dockerfile:\n%s", dfD)
		if err := os.WriteFile(filepath.Join(dfDirD, "Dockerfile"), []byte(dfD), 0o644); err != nil {
			t.Fatalf("Part D: write Dockerfile: %v", err)
		}
		dfFSD, dfFSErrD := fsutil.NewFS(dfDirD)
		if dfFSErrD != nil {
			t.Fatalf("Part D: fsutil.NewFS(dfDir): %v", dfFSErrD)
		}

		outDirD := t.TempDir()
		ctxD, cancelDctx := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelDctx()

		solveCtxD, cancelCauseD := context.WithCancelCause(ctxD)
		defer cancelCauseD(nil)
		sfsSetD := builder.NewSizeVerifiedSet(cancelCauseD)

		startD := time.Now()
		errD := builder.ExportAndUnpack(ctxD, outDirD, func(egCtx context.Context, pw io.WriteCloser) error {
			bk, bkErr := bkclient.New(egCtx, buildkitHost)
			if bkErr != nil {
				return fmt.Errorf("bkclient.New: %w", bkErr)
			}
			defer bk.Close()
			_, solveErr := bk.Solve(solveCtxD, nil, bkclient.SolveOpt{
				LocalMounts: map[string]fsutil.FS{
					"context":     sfsSetD.Wrap(ctxFSD),
					"dockerfile":  sfsSetD.Wrap(dfFSD),
					"nexus3agent": sfsSetD.Wrap(agentCutD),
				},
				Frontend: "dockerfile.v0",
				FrontendAttrs: map[string]string{
					"filename":            "Dockerfile",
					"context:nexus3agent": "local:nexus3agent",
				},
				Exports: []bkclient.ExportEntry{{
					Type: bkclient.ExporterTar,
					Output: func(_ map[string]string) (io.WriteCloser, error) {
						return pw, nil
					},
				}},
			}, nil)
			return solveErr
		})
		elapsedD := time.Since(startD)
		if fsErrD := sfsSetD.Err(); fsErrD != nil {
			errD = fsErrD
		}

		if errD == nil {
			t.Fatal("Part D: expected non-nil error from nexus3agent truncation guard, got nil")
		}
		t.Logf("Part D: elapsed=%s error: %v", elapsedD.Round(time.Second), errD)

		if !strings.Contains(errD.Error(), "_nexus3-agent") {
			t.Errorf("Part D: error does not mention agent path: %s", errD)
		}
		if !strings.Contains(errD.Error(), "4194304") {
			t.Errorf("Part D: error does not mention got-bytes 4194304: %s", errD)
		}
		if elapsedD > 2*time.Minute {
			t.Errorf("Part D: Solve took %s — cancel-cause not propagating fast enough", elapsedD.Round(time.Second))
		}
		// No agent binary should appear in the output since the build failed.
		if fi, statErr := os.Stat(filepath.Join(outDirD, "sbin", "nexus3-agent")); statErr == nil {
			t.Errorf("Part D: /sbin/nexus3-agent unexpectedly in outDir (size=%d) — guard did not stop the build", fi.Size())
		}
	}

	// ── Part D mutation: raw agentFS → Solve succeeds, agent file is 4 MiB ────
	// Without the guard, buildkitd accepts the short read silently: the build
	// succeeds and the agent binary inside the image is exactly 4194304 bytes.
	t.Log("Part D mutation: raw agentFS without sfsSet — Solve must succeed with 4 MiB truncated agent")
	{
		agentBinDM := w34BuildNexus3Agent(t)
		agentBytesDM, _ := os.ReadFile(agentBinDM)

		agentDirDM := t.TempDir()
		if err := os.WriteFile(filepath.Join(agentDirDM, "_nexus3-agent"), agentBytesDM, 0o755); err != nil {
			t.Fatalf("Part D mut: write agent: %v", err)
		}
		agentFSDM_real, _ := fsutil.NewFS(agentDirDM)
		agentCutDM := &cutFS{inner: agentFSDM_real, cutAt: 4 << 20}

		ctxDirDM := t.TempDir()
		ctxFSDM, _ := fsutil.NewFS(ctxDirDM)

		dfDirDM := t.TempDir()
		dfDM := "FROM alpine:latest\n" +
			fmt.Sprintf("RUN echo %d > /.nexus3-w39-probe-DM\n", time.Now().UnixNano()) +
			"COPY --chmod=0755 --from=nexus3agent _nexus3-agent /sbin/nexus3-agent\n"
		if err := os.WriteFile(filepath.Join(dfDirDM, "Dockerfile"), []byte(dfDM), 0o644); err != nil {
			t.Fatalf("Part D mut: write Dockerfile: %v", err)
		}
		dfFSDM, _ := fsutil.NewFS(dfDirDM)

		outDirDM := t.TempDir()
		ctxDM, cancelDMctx := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelDMctx()

		startDM := time.Now()
		errDM := builder.ExportAndUnpack(ctxDM, outDirDM, func(egCtx context.Context, pw io.WriteCloser) error {
			bk, bkErr := bkclient.New(egCtx, buildkitHost)
			if bkErr != nil {
				return fmt.Errorf("bkclient.New: %w", bkErr)
			}
			defer bk.Close()
			_, solveErr := bk.Solve(ctxDM, nil, bkclient.SolveOpt{
				LocalMounts: map[string]fsutil.FS{
					"context":     ctxFSDM,
					"dockerfile":  dfFSDM,
					"nexus3agent": agentCutDM, // RAW — no sfsSet wrapping
				},
				Frontend: "dockerfile.v0",
				FrontendAttrs: map[string]string{
					"filename":            "Dockerfile",
					"context:nexus3agent": "local:nexus3agent",
				},
				Exports: []bkclient.ExportEntry{{
					Type: bkclient.ExporterTar,
					Output: func(_ map[string]string) (io.WriteCloser, error) {
						return pw, nil
					},
				}},
			}, nil)
			return solveErr
		})
		elapsedDM := time.Since(startDM)

		if errDM != nil {
			t.Logf("Part D mutation: Solve failed (buildkitd may now validate sizes): %v", errDM)
			t.Log("NOTE: Part D mutation inconclusive — buildkitd rejected the short read before sfsSet could")
			return
		}
		t.Logf("MUTATION Part D: raw agentFS — Solve succeeded in %s (expected)", elapsedDM.Round(time.Second))

		// The unpacked /sbin/nexus3-agent must be exactly 4 MiB (truncated).
		agentOutPath := filepath.Join(outDirDM, "sbin", "nexus3-agent")
		fi, statErr := os.Stat(agentOutPath)
		if statErr != nil {
			t.Logf("MUTATION Part D: /sbin/nexus3-agent not in unpacked outDir: %v", statErr)
		} else {
			t.Logf("MUTATION Part D: /sbin/nexus3-agent size=%d bytes (%.1f MiB)", fi.Size(), float64(fi.Size())/(1<<20))
			if fi.Size() != 4<<20 {
				t.Errorf("MUTATION Part D: expected truncated size 4194304, got %d", fi.Size())
			}
		}

		// Pack into ext4 and run debugfs to confirm Size: 4194304.
		ext4DM := filepath.Join(t.TempDir(), "agent-truncated.ext4")
		const ext4SizeMiB = 256
		if err := builder.RunMke2fs(ctxDM, outDirDM, ext4DM, ext4SizeMiB<<20); err != nil {
			t.Logf("MUTATION Part D: mke2fs failed: %v (skipping debugfs check)", err)
		} else {
			dbOut, dbErr := exec.Command("debugfs", "-R", "stat /sbin/nexus3-agent", ext4DM).CombinedOutput()
			if dbErr != nil {
				t.Logf("MUTATION Part D: debugfs unavailable: %v\n%s", dbErr, dbOut)
			} else {
				t.Logf("MUTATION Part D: debugfs stat /sbin/nexus3-agent:\n%s", dbOut)
				if !strings.Contains(string(dbOut), "4194304") {
					t.Errorf("MUTATION Part D: debugfs output does not show truncated size 4194304:\n%s", dbOut)
				}
			}
		}
	}
}
