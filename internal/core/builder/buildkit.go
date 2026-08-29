package builder

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	ctdarchive "github.com/containerd/containerd/archive"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"

	"github.com/IniZio/nexus3/internal/core/bootspec"
)

// agentContextFilenamePrefix is the reserved name prefix used for the
// nexus3-agent binary inside the buildkit "nexus3agent" named context. Using a
// leading underscore avoids collisions with typical workspace filenames.
const agentContextFilenamePrefix = "_nexus3-agent"

// newAgentContextFilename returns a per-Solve unique name for the agent binary
// inside the "nexus3agent" named build context.
//
// # Why the name must be unique per Solve
//
// buildkitd caches the RESULT SNAPSHOT of the final
// `COPY --from=nexus3agent` under a cache key derived from the copied file's
// contenthash (buildkit cache/contenthash/filehash.go NewFromStat → tarsum v1,
// which excludes mtime). The agent binary's path, size, mode and content are
// identical from one build to the next, so that key is STABLE across builds —
// and a result snapshot that was written with a CORRUPT (zero-byte) agent is
// therefore returned forever.
//
// That is not hypothetical. On cache-disk slot 0
// (~/.local/state/nexus3/caches/buildkit.ext4), snapshot 61 held a zero-byte
// /usr/sbin/nexus3-agent written at 15:05 on 2026-08-29. Every later build of
// the same Containerfile cache-hit that snapshot, finished the whole solve in
// ~7 s without re-executing a single layer, and then failed the
// verifyAgentIntegrity canary with "/sbin/nexus3-agent is 0 bytes, expected
// 36329665". The canary is fail-closed, so the poisoned layer never shipped —
// but it also never healed: the only escape was deleting the operator's warm
// cache disk. sizeVerifiedFS (sizedfs.go) cannot help here, because it guards
// the context STREAM and the stream is healthy; the corruption lives in a
// persisted buildkitd snapshot.
//
// Putting a fresh nonce in the COPY source path makes the agent layer's cache
// key unique per build, so a poisoned agent layer can never be served a second
// time. Only this final layer re-executes; every Containerfile layer above it
// still cache-hits, preserving the layer ordering rationale documented on
// [realBuildkitClient.Solve].
//
// A crypto/rand failure is FATAL, not degraded: any process-local fallback
// (a counter) restarts in every new process and so collides across
// processes, silently reinstating the stable-key poisoned-snapshot class
// this function exists to eliminate.
func newAgentContextFilename() (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("buildkit: agent layer nonce: crypto/rand: %w", err)
	}
	return fmt.Sprintf("%s-%s", agentContextFilenamePrefix, hex.EncodeToString(nonce[:])), nil
}

// stageAgentContext copies the agent binary at agentPath into agentDir under a
// fresh per-Solve nonce name and returns that name. The returned name is the
// ONLY thing [synthesizeDockerfile] may use as the COPY source: the write and
// the COPY line are coupled through this single return value, so they cannot
// silently diverge (which would fail every build with "file not found").
func stageAgentContext(agentDir, agentPath string) (string, error) {
	agentFile, err := newAgentContextFilename()
	if err != nil {
		return "", err
	}
	agentBytes, err := os.ReadFile(agentPath)
	if err != nil {
		return "", fmt.Errorf("buildkit: read agent binary %s: %w", agentPath, err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, agentFile), agentBytes, 0755); err != nil {
		return "", fmt.Errorf("buildkit: write agent to agent dir: %w", err)
	}
	return agentFile, nil
}

// synthesizeDockerfile returns the combined Dockerfile handed to the
// dockerfile.v0 frontend: the user's Containerfile followed by the final layer
// that installs the agent binary from the "nexus3agent" named context.
//
// agentFile must come from [newAgentContextFilename] — the name is what makes
// the agent layer's buildkit cache key unique per build.
func synthesizeDockerfile(containerfileBytes []byte, agentFile, installPath string) []byte {
	finalLayer := fmt.Sprintf(
		"\n\n# Final layer: bake the nexus3-agent (boot contract: init=%s)\nCOPY --chmod=0755 --from=nexus3agent %s %s\n",
		installPath, agentFile, installPath,
	)
	return append(append([]byte(nil), containerfileBytes...), []byte(finalLayer)...)
}

// SolveRequest is the fully-resolved build specification handed to a
// [BuildkitClient]. It carries everything needed to describe one complete
// build without any filesystem traversal on the client side.
type SolveRequest struct {
	// BaseRef is the OCI image reference used as the FROM base, e.g.
	// "debian:bookworm-slim".
	BaseRef string

	// ContainerfileBytes is the content of the project's .nexus/Containerfile,
	// applied on top of BaseRef.
	ContainerfileBytes []byte

	// AgentPath is the host filesystem path to the nexus3-agent binary.
	AgentPath string

	// AgentInstallPath is the absolute in-guest path where the agent binary is
	// placed as the final layer. Always "/sbin/nexus3-agent" in production.
	AgentInstallPath string

	// WorkspaceDir is the absolute host path to the project workspace root.
	// It is used as the buildkit build context so that COPY instructions in
	// the user's Containerfile can reference files from the repo.
	WorkspaceDir string
}

// BuildkitClient is the seam between [Builder] and a running buildkitd daemon.
//
// # Production implementation
//
// [realBuildkitClient] wraps github.com/moby/buildkit/client. It synthesises a
// combined Dockerfile (user's Containerfile + agent-as-final-layer COPY) and
// drives the build via the Dockerfile frontend, exporting the result as a local
// filesystem directory.
//
// # Test implementation
//
// Tests supply a fake that records the [SolveRequest] and populates outDir with
// a minimal synthetic tree. No buildkitd process or network access is required.
//
// # Seam contract
//
// Solve MUST:
//   - Start the build from req.BaseRef.
//   - Apply req.ContainerfileBytes instructions on top of the base.
//   - Install req.AgentPath as req.AgentInstallPath (mode 0755) as the final
//     layer, encoding the boot contract init=<AgentInstallPath>.
//   - Write the resulting POSIX filesystem tree into outDir (caller-owned).
type BuildkitClient interface {
	// Solve drives one build against a remote buildkitd, writing the resulting
	// filesystem tree into outDir. outDir is created by the caller; Solve
	// populates it.
	Solve(ctx context.Context, req SolveRequest, outDir string) error
}

// realBuildkitClient wraps github.com/moby/buildkit/client and implements
// [BuildkitClient].
//
// # Bootstrap note
//
// Calling Solve requires a running buildkitd reachable at c.addr. The
// intended runtime topology is:
//
//  1. nexus3 host calls driver.Start with the builder VM image
//     (images/builder/Containerfile — stock moby/buildkit) and waits for
//     the VM to report Running.
//  2. The host dials buildkitd's gRPC socket via driver.GuestDialer (vsock
//     or TCP forward) and obtains a host-side address.
//  3. That address is passed here as c.addr.
//  4. After the build, the host calls driver.Stop on the builder VM.
//
// This lifecycle (boot → connect → build → stop) is the integration slice
// that wires Builder with internal/core/driver; it is out of scope for S2.
type realBuildkitClient struct {
	addr string
}

// exportAndUnpack drives a buildkit solve function and unpacks the resulting tar
// stream into outDir concurrently via an errgroup.
//
// # Concurrency
//
// Two goroutines run simultaneously:
//   - The solve goroutine calls solveFn, which must write the complete tar stream
//     into pw and then return. exportAndUnpack closes pw on success, or closes it
//     with the error on failure, so that the unpack goroutine always sees EOF or an
//     error and cannot deadlock.
//   - The unpack goroutine calls containerd/archive.Apply, which reads from pr and
//     applies each tar entry to outDir.
//
// # Fail-closed guarantee
//
// containerd/archive.Apply uses stdlib archive/tar internally. A tar entry whose
// body is shorter than hdr.Size causes an io.ErrUnexpectedEOF on the body read,
// which Apply returns as a hard error — never silently truncated output.
// Additionally, Apply returns an error on EPERM when setting security.* xattrs
// (e.g. security.capability), so a host that lacks the required privilege will
// produce a hard build failure rather than a bootable image missing capabilities.
//
// # solveFn contract
//
// solveFn receives the errgroup-derived context (egCtx) and the pipe writer pw.
// solveFn MUST NOT close pw — exportAndUnpack closes it after solveFn returns.
// solveFn SHOULD use egCtx so that an unpack failure (which cancels egCtx)
// interrupts the build and avoids deadlock.
func exportAndUnpack(ctx context.Context, outDir string, solveFn func(egCtx context.Context, pw io.WriteCloser) error) error {
	pr, pw := io.Pipe()

	eg, egCtx := errgroup.WithContext(ctx)

	// Unpack goroutine: reads the tar stream from pr and applies it to outDir.
	// WithNoSameOwner matches the parity of the old ExporterLocal path: buildkit's
	// client-side receive filter (session/filesync/diffcopy.go:119-124) rewrote
	// every uid/gid to the current user before writing, so the old outDir was
	// entirely owned by the builder uid. We preserve that behaviour by skipping
	// lchown. Security xattrs (security.capability etc.) are left at Apply's
	// default: user.* EPERM is warned-and-skipped; security.* EPERM is an error.
	// Neither appears in real exports: rootless buildkitd strips device nodes and
	// security xattrs from the tar entirely (confirmed by inspection of a live
	// alpine export: 0 TypeChar/Block entries, 0 security.* PAX headers).
	// On failure, CloseWithError signals the solve side to stop writing (prevents
	// deadlock if Apply returns early before the full stream is consumed).
	eg.Go(func() error {
		n, err := ctdarchive.Apply(egCtx, outDir, pr, ctdarchive.WithNoSameOwner())
		if err != nil {
			pw.CloseWithError(err)
			return fmt.Errorf("buildkit: unpack tar to %s: %w", outDir, err)
		}
		slog.Info("buildkit: exportAndUnpack: tar unpacked", "outDir", outDir, "bytesWritten", n)
		return nil
	})

	// Solve goroutine: calls the user-supplied function that writes the tar stream
	// into pw. On completion, close pw so Apply's reader sees EOF (or the error).
	eg.Go(func() error {
		slog.Debug("buildkit: exportAndUnpack: streaming tar export to unpack goroutine", "outDir", outDir)
		err := solveFn(egCtx, pw)
		if err != nil {
			pw.CloseWithError(err)
			return err
		}
		pw.Close()
		return nil
	})

	// Join both goroutines. Returns the first non-nil error from either side,
	// which includes unpack errors propagated back to the caller.
	return eg.Wait()
}

// NewBuildkitClient constructs a [BuildkitClient] that connects to buildkitd
// at addr on each Solve call. addr must be in the form accepted by
// github.com/moby/buildkit/client.New, e.g.
//   - "unix:///run/buildkit/buildkitd.sock"
//   - "tcp://127.0.0.1:1234"
func NewBuildkitClient(addr string) (BuildkitClient, error) {
	if addr == "" {
		return nil, fmt.Errorf("buildkit: addr is required")
	}
	return &realBuildkitClient{addr: addr}, nil
}

// buildLocalMounts constructs the three LocalMounts entries used by every
// Solve call, wrapping EVERY FS with the supplied sizeVerifiedSet so that a
// truncated read on any mount — including the nexus3agent binary (the artifact
// class that triggered the 32 MiB production truncation) — immediately cancels
// the Solve context.
//
// Keeping the map construction in one place is the regression guard: a future
// edit that replaces one Wrap call with a raw FS will be caught by
// TestBuildLocalMounts_AllWrapped in plain `go test`, without a live buildkitd.
func buildLocalMounts(set *sizeVerifiedSet, ctxFS, dfFS, agentFS fsutil.FS) map[string]fsutil.FS {
	return map[string]fsutil.FS{
		"context":     set.Wrap(ctxFS),
		"dockerfile":  set.Wrap(dfFS),
		"nexus3agent": set.Wrap(agentFS),
	}
}

// Solve implements [BuildkitClient].
//
// It connects to buildkitd, synthesises a combined Dockerfile that applies
// req.ContainerfileBytes on top of req.BaseRef and then installs the agent
// binary as the final layer, then drives the build via the Dockerfile v0
// frontend and exports the filesystem into outDir.
//
// The synthetic Dockerfile follows the pattern:
//
//	<content of req.ContainerfileBytes>
//
//	# Final layer: bake the nexus3-agent (boot contract: init=/sbin/nexus3-agent)
//	COPY --chmod=0755 --from=nexus3agent _nexus3-agent-<nonce> <req.AgentInstallPath>
//
// Placing the agent COPY last means an agent version bump only invalidates
// that single layer; all Containerfile layers above are cache-hits in buildkitd.
// The <nonce> in the source filename ([newAgentContextFilename]) makes that
// last layer a deliberate cache MISS on every build, so a corrupt agent layer
// can never be served from buildkitd's persisted snapshot cache.
//
// # Context layout
//
// Three buildkit local sources are used:
//
//   - "context":        req.WorkspaceDir (passed to buildkitd directly, no
//     intermediate copy; buildkitd's fsutil handles symlinks and large trees).
//   - "dockerfile":     a small temp dir containing only the synthetic Dockerfile.
//   - "nexus3agent":   a small temp dir containing only the agent binary,
//     referenced via the buildkitd named-context feature so the workspace
//     directory never needs to be polluted with nexus3 internals.
func (c *realBuildkitClient) Solve(ctx context.Context, req SolveRequest, outDir string) error {
	// Connect to buildkitd (one connection per Solve; acceptable for the
	// current slice — a connection pool is an optimisation for later).
	bk, err := bkclient.New(ctx, c.addr)
	if err != nil {
		return fmt.Errorf("buildkit: connect %s: %w", c.addr, err)
	}
	defer bk.Close()

	// Synthesise a combined Dockerfile: user instructions + agent final layer.
	// The agent COPY uses a named build context (nexus3agent) so the agent
	// binary does not need to reside in the workspace context. The source
	// filename carries a per-Solve nonce so the agent layer is never served
	// from a stale buildkitd result snapshot — see [newAgentContextFilename].
	// Small temp dir for the agent binary, used as the "nexus3agent" named
	// build context. This avoids writing nexus3 internals into the workspace.
	agentDir, err := os.MkdirTemp("", "nexus3-bkagent-*")
	if err != nil {
		return fmt.Errorf("buildkit: create agent dir: %w", err)
	}
	defer os.RemoveAll(agentDir)
	agentFile, err := stageAgentContext(agentDir, req.AgentPath)
	if err != nil {
		return err
	}
	synthDF := synthesizeDockerfile(req.ContainerfileBytes, agentFile, req.AgentInstallPath)

	// Small temp dir for the synthetic Dockerfile only.
	dfDir, err := os.MkdirTemp("", "nexus3-bkdf-*")
	if err != nil {
		return fmt.Errorf("buildkit: create dockerfile dir: %w", err)
	}
	defer os.RemoveAll(dfDir)
	if err := os.WriteFile(filepath.Join(dfDir, "Dockerfile"), synthDF, 0600); err != nil {
		return fmt.Errorf("buildkit: write Dockerfile: %w", err)
	}

	// Wire build context: workspace is passed to buildkitd directly (no
	// intermediate copy). When WorkspaceDir is empty, fall back to the
	// Dockerfile dir so COPY instructions that only reference the
	// Dockerfile's own synthetic files still compile.
	ctxDir := req.WorkspaceDir
	if ctxDir == "" {
		ctxDir = dfDir
	}

	// Build FS handles for the three local build contexts. ctxFS is wrapped
	// inside the exportAndUnpack closure where the Solve cancel-cause is
	// available (D-8: see sizedfs.go for the mechanism).
	ctxFS, err := fsutil.NewFS(ctxDir)
	if err != nil {
		return fmt.Errorf("buildkit: create context FS: %w", err)
	}
	dfFS, err := fsutil.NewFS(dfDir)
	if err != nil {
		return fmt.Errorf("buildkit: create dockerfile FS: %w", err)
	}
	agentFS, err := fsutil.NewFS(agentDir)
	if err != nil {
		return fmt.Errorf("buildkit: create agent FS: %w", err)
	}

	// Export via tar exporter + host-side unpack. ExporterTar is fail-closed:
	// stdlib archive/tar reports a hard error if any entry body is shorter than
	// its declared size, unlike ExporterLocal (tonistiigi/fsutil sendFile) which
	// silently produces a truncated file on a short source read.
	err = exportAndUnpack(ctx, outDir, func(egCtx context.Context, pw io.WriteCloser) error {
		// solveCtx is cancelled by the first size violation across ANY of the
		// three local mounts so bk.Solve tears down within seconds (sizedfs.go
		// explains why the default deadline-wait behaviour masks the fault as a
		// flaky timeout).  All three mounts share one sizeVerifiedSet so a
		// truncated read on the nexus3agent binary (the artifact class that
		// triggered the 32 MiB production truncation) is caught as quickly as
		// a violation on the build context.
		solveCtx, cancelCause := context.WithCancelCause(egCtx)
		defer cancelCause(nil)
		sfsSet := newSizeVerifiedSet(cancelCause)
		_, solveErr := bk.Solve(solveCtx, nil, bkclient.SolveOpt{
			LocalMounts: buildLocalMounts(sfsSet, ctxFS, dfFS, agentFS),
			FrontendAttrs: map[string]string{
				// Tell the Dockerfile frontend which file to use.
				"filename": "Dockerfile",
				// Register the agent binary dir as a named build context so
				// the final-layer COPY --from=nexus3agent resolves correctly.
				"context:nexus3agent": "local:nexus3agent",
			},
			Frontend: "dockerfile.v0",
			Exports: []bkclient.ExportEntry{
				{
					// ExporterTar streams the rootfs as a tar archive into pw.
					// Output is a func returning pw so buildkit can open the
					// writer after negotiating export metadata with the daemon.
					Type: bkclient.ExporterTar,
					Output: func(_ map[string]string) (io.WriteCloser, error) {
						return pw, nil
					},
				},
			},
		}, nil)
		// A size violation cancels solveCtx, making solveErr a context error.
		// Return our descriptive error in preference so the caller sees the
		// path, got-bytes, and expected-bytes rather than "context canceled".
		if fsErr := sfsSet.Err(); fsErr != nil {
			return fmt.Errorf("buildkit: solve: %w", fsErr)
		}
		if solveErr != nil {
			return fmt.Errorf("buildkit: solve: %w", solveErr)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Parse the Containerfile directly and write boot.json into the exported
	// rootfs. This is the authoritative path: it does not depend on buildkitd
	// version, exporter type, or gateway metadata availability.
	// Non-fatal: a parse error must never fail the build (rootfs export succeeded).
	captureBootSpecFromContainerfile(req.ContainerfileBytes, outDir)

	return nil
}

// captureBootSpecFromContainerfile parses containerfileBytes (the raw content of
// the user's .nexus/Containerfile) and, when an ENTRYPOINT or CMD is declared,
// writes a boot.json into the exported rootfs at <outDir>/etc/nexus3/boot.json.
//
// # Mechanism
//
// This function uses buildkit's own Dockerfile parser and instructions packages
// to extract ENTRYPOINT, CMD, WORKDIR, and ENV from the last build stage. Both
// exec-form (JSON array) and shell-form instructions are handled via
// ShellDependantCmdLine.PrependShell.
//
// # Design rationale
//
// Parsing the Containerfile directly is deterministic and buildkitd-version-
// independent. The previous mechanism read OCI image config from
// gateway.Result.Metadata, which moby/buildkit v0.19 (the in-guest buildkitd
// version) does not populate for ExporterLocal builds. The live VM-boot e2e
// proved this: boot.json was never written in-guest because the key was absent.
//
// # Limitation
//
// Only instructions DECLARED IN THE USER'S .nexus/Containerfile are captured —
// config inherited from the base image (FROM) is NOT included. This is
// intentional for nexus3: the operator is expected to re-declare any base-image
// ENTRYPOINT/CMD they want the nexus3 boot contract to honour. Incidental base
// defaults (e.g. ubuntu's CMD ["bash"]) are silently ignored, which is correct.
//
// # Edge case: shell-form ENTRYPOINT with CMD
//
// In Docker, a shell-form ENTRYPOINT (PrependShell=true) receives its own shell
// wrapper (/bin/sh -c) and ignores any CMD entirely. This function honours that
// by dropping CMD when the resolved entrypoint is shell-wrapped.
//
// All failures are NON-FATAL: a parse error must never fail the build (the
// rootfs export already succeeded).
func captureBootSpecFromContainerfile(containerfileBytes []byte, outDir string) {
	if len(containerfileBytes) == 0 {
		slog.Debug("buildkit: captureBootSpecFromContainerfile: empty Containerfile, skipping boot.json")
		return
	}

	result, err := parser.Parse(bytes.NewReader(containerfileBytes))
	if err != nil {
		slog.Warn("buildkit: captureBootSpecFromContainerfile: failed to parse Containerfile, skipping boot.json", "err", err)
		return
	}

	stages, _, err := instructions.Parse(result.AST, nil)
	if err != nil {
		slog.Warn("buildkit: captureBootSpecFromContainerfile: failed to parse Containerfile instructions, skipping boot.json", "err", err)
		return
	}
	if len(stages) == 0 {
		slog.Debug("buildkit: captureBootSpecFromContainerfile: no stages in Containerfile, skipping boot.json")
		return
	}

	// Walk only the last (final) stage — that is the image that will be run.
	last := stages[len(stages)-1]

	var (
		entrypointCmd *instructions.EntrypointCommand
		cmdCmd        *instructions.CmdCommand
		workdir       string
		envPairs      []string
	)
	for _, cmd := range last.Commands {
		switch c := cmd.(type) {
		case *instructions.EntrypointCommand:
			entrypointCmd = c
		case *instructions.CmdCommand:
			cmdCmd = c
		case *instructions.WorkdirCommand:
			workdir = c.Path
		case *instructions.EnvCommand:
			for _, kv := range c.Env {
				envPairs = append(envPairs, kv.Key+"="+kv.Value)
			}
		}
	}

	// Resolve ENTRYPOINT and CMD to concrete argv, honouring shell form.
	resolveArgv := func(s instructions.ShellDependantCmdLine) []string {
		if s.PrependShell {
			return []string{"/bin/sh", "-c", strings.Join(s.CmdLine, " ")}
		}
		return s.CmdLine
	}

	var entrypointArgv, cmdArgv []string
	if entrypointCmd != nil {
		entrypointArgv = resolveArgv(entrypointCmd.ShellDependantCmdLine)
	}
	if cmdCmd != nil {
		// Shell-form ENTRYPOINT ignores CMD entirely (Docker semantics).
		if entrypointCmd == nil || !entrypointCmd.PrependShell {
			cmdArgv = resolveArgv(cmdCmd.ShellDependantCmdLine)
		}
	}

	ociCfg := bootspec.OCIImageConfig{
		Entrypoint: entrypointArgv,
		Cmd:        cmdArgv,
		WorkingDir: workdir,
		Env:        envPairs,
	}
	spec := bootspec.FromOCIImageConfig(ociCfg)
	if len(spec.Tasks) == 0 {
		slog.Debug("buildkit: captureBootSpecFromContainerfile: no entrypoint/cmd declared, skipping boot.json")
		return
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		slog.Warn("buildkit: captureBootSpecFromContainerfile: failed to marshal boot spec, skipping boot.json", "err", err)
		return
	}

	bootJSONPath := filepath.Join(outDir, "etc", "nexus3", "boot.json")
	if err := os.MkdirAll(filepath.Dir(bootJSONPath), 0755); err != nil {
		slog.Warn("buildkit: captureBootSpecFromContainerfile: failed to create boot.json parent dirs, skipping", "err", err)
		return
	}
	if err := os.WriteFile(bootJSONPath, specJSON, 0644); err != nil {
		slog.Warn("buildkit: captureBootSpecFromContainerfile: failed to write boot.json, skipping", "err", err)
		return
	}
	slog.Info("buildkit: captureBootSpecFromContainerfile: wrote boot.json", "path", bootJSONPath, "tasks", len(spec.Tasks))
}

// copyDirIntoContext recursively copies all files from src into dst, preserving
// relative paths. Symlinks are resolved; if a symlink target escapes src (i.e.
// its real path is not rooted at src), the symlink is skipped to prevent
// workspace-escape attacks. Reserved filenames (Dockerfile, agentContextFilenamePrefix)
// are not skipped here — the caller overwrites them after this function returns.
func copyDirIntoContext(src, dst string) error {
	// Resolve src to a canonical path for escape detection.
	srcReal, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolve src %s: %w", src, err)
	}

	return filepath.WalkDir(srcReal, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Derive destination path.
		rel, err := filepath.Rel(srcReal, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		// For symlinks: resolve target and verify it stays within src.
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				// Broken symlink — skip silently.
				return nil //nolint:nilerr
			}
			targetReal, err := filepath.EvalSymlinks(target)
			if err != nil {
				return nil //nolint:nilerr
			}
			// If the resolved target escapes the workspace root, skip it.
			if !isDescendant(srcReal, targetReal) {
				return nil
			}
			// Copy the target file contents rather than recreating the symlink.
			path = targetReal
		}

		return copyFile(path, dstPath)
	})
}

// isDescendant reports whether child is equal to or nested under parent.
// Both paths must be absolute and clean (EvalSymlinks output).
func isDescendant(parent, child string) bool {
	if parent == child {
		return true
	}
	return len(child) > len(parent) &&
		child[len(parent)] == filepath.Separator &&
		child[:len(parent)] == parent
}

// copyFile copies the regular file at src to dst, creating dst's parent
// directory if necessary.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
