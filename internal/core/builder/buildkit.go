package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"github.com/IniZio/nexus3/internal/core/bootspec"
)

// agentContextFilename is the reserved name used for the nexus3-agent binary
// inside the buildkit context directory. Using a leading underscore avoids
// collisions with typical workspace filenames.
const agentContextFilename = "_nexus3-agent"

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
//	COPY --chmod=0755 --from=_nexus3_agent _nexus3-agent <req.AgentInstallPath>
//
// Placing the agent COPY last means an agent version bump only invalidates
// that single layer; all Containerfile layers above are cache-hits in buildkitd.
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
	// binary does not need to reside in the workspace context.
	finalLayer := fmt.Sprintf(
		"\n\n# Final layer: bake the nexus3-agent (boot contract: init=%s)\nCOPY --chmod=0755 --from=nexus3agent %s %s\n",
		req.AgentInstallPath, agentContextFilename, req.AgentInstallPath,
	)
	synthDF := append(append([]byte(nil), req.ContainerfileBytes...), []byte(finalLayer)...)

	// Small temp dir for the synthetic Dockerfile only.
	dfDir, err := os.MkdirTemp("", "nexus3-bkdf-*")
	if err != nil {
		return fmt.Errorf("buildkit: create dockerfile dir: %w", err)
	}
	defer os.RemoveAll(dfDir)
	if err := os.WriteFile(filepath.Join(dfDir, "Dockerfile"), synthDF, 0600); err != nil {
		return fmt.Errorf("buildkit: write Dockerfile: %w", err)
	}

	// Small temp dir for the agent binary, used as the "nexus3agent" named
	// build context. This avoids writing nexus3 internals into the workspace.
	agentDir, err := os.MkdirTemp("", "nexus3-bkagent-*")
	if err != nil {
		return fmt.Errorf("buildkit: create agent dir: %w", err)
	}
	defer os.RemoveAll(agentDir)
	agentBytes, err := os.ReadFile(req.AgentPath)
	if err != nil {
		return fmt.Errorf("buildkit: read agent binary %s: %w", req.AgentPath, err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, agentContextFilename), agentBytes, 0755); err != nil {
		return fmt.Errorf("buildkit: write agent to agent dir: %w", err)
	}

	// Wire build context: workspace is passed to buildkitd directly (no
	// intermediate copy). When WorkspaceDir is empty, fall back to the
	// Dockerfile dir so COPY instructions that only reference the
	// Dockerfile's own synthetic files still compile.
	ctxDir := req.WorkspaceDir
	if ctxDir == "" {
		ctxDir = dfDir
	}

	_, err = bk.Solve(ctx, nil, bkclient.SolveOpt{
		// LocalDirs is deprecated in favour of LocalMounts, but it remains
		// fully supported in moby/buildkit v0.18 and its replacement
		// (fsutil.FS) would add a heavier import for no benefit here.
		LocalDirs: map[string]string{
			"context":     ctxDir,
			"dockerfile":  dfDir,
			"nexus3agent": agentDir,
		},
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
				Type:      bkclient.ExporterLocal,
				OutputDir: outDir,
			},
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("buildkit: solve: %w", err)
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
// workspace-escape attacks. Reserved filenames (Dockerfile, agentContextFilename)
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
