package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

// toolFetchFn is the fetch function signature used by resolveSandboxTools.
type toolFetchFn func(ctx context.Context, t toolcache.Tool, goarch string) (toolcache.Fetched, error)

// resolveSandboxTools returns host-verified tools to inject, or nil when opted
// out (optOut=true or containerfile contains the skip directive). Each fetch is
// bounded to 90 s so an air-gapped host does not hang sandbox create. Fetch
// failures are logged at Warn level; checksum mismatches at Error level with
// the phrase "not installed". Failed tools are silently dropped.
func resolveSandboxTools(ctx context.Context, optOut bool, containerfile []byte, goarch string, fetch toolFetchFn) []toolcache.Fetched {
	if optOut {
		return nil
	}
	if toolcache.SkippedBy(containerfile) {
		return nil
	}

	defaults := toolcache.Defaults()
	out := make([]toolcache.Fetched, 0, len(defaults))
	for _, t := range defaults {
		fetchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		fetched, err := fetch(fetchCtx, t, goarch)
		cancel()
		if err != nil {
			if errors.Is(err, toolcache.ErrChecksumMismatch) {
				slog.ErrorContext(ctx, "sandbox tool checksum mismatch, not installed",
					"tool", t.Name, "error", err)
			} else {
				slog.WarnContext(ctx, "sandbox tool fetch failed, skipping",
					"tool", t.Name, "error", err)
			}
			continue
		}
		out = append(out, fetched)
	}
	return out
}

// defaultToolFetch returns a toolFetchFn backed by a toolcache.Fetcher whose
// root is filepath.Join(storeRoot, "tools").
func defaultToolFetch(storeRoot string) toolFetchFn {
	f := toolcache.Fetcher{Root: filepath.Join(storeRoot, "tools")}
	return f.Fetch
}

// sandboxToolsOptOutFromConfig returns true when the user has explicitly
// disabled the gh tool (cfg.Sandbox.Tools.GH set to false).
// A nil pointer (absent key) means "default on".
func sandboxToolsOptOutFromConfig(cfg config.Config) bool {
	return cfg.Sandbox.Tools.GH != nil && !*cfg.Sandbox.Tools.GH
}

// userGlobalSandboxToolsOptOut reports whether the user-global config has opted
// out of sandbox-tool injection. It is a package var so tests can stub it.
// The default implementation loads the user-global config and delegates to
// sandboxToolsOptOutFromConfig; on load error it returns false (default on).
var userGlobalSandboxToolsOptOut = func() bool {
	cfg, err := config.LoadUserGlobal()
	if err != nil {
		return false
	}
	return sandboxToolsOptOutFromConfig(cfg)
}

// imageOptsSandboxTools returns the tools to inject for an OCI-image-booted
// sandbox. It returns opts.SandboxTools unchanged when non-nil. It returns nil
// when opts.Image.RootfsPath is set (opaque ext4), when opts.Image.Ref is
// empty (digest-only or no image), or when Ref is a bare "sha256:" digest
// string (digest path does no pull so there is nothing to inject into).
// Otherwise it delegates to resolveSandboxTools.
func imageOptsSandboxTools(ctx context.Context, opts service.CreateAndBootOptions, optOut bool, goarch string, fetch toolFetchFn) []toolcache.Fetched {
	if opts.SandboxTools != nil {
		return opts.SandboxTools
	}
	if opts.Image.RootfsPath != "" {
		return nil
	}
	if opts.Image.Ref == "" {
		return nil
	}
	if strings.HasPrefix(opts.Image.Ref, "sha256:") {
		return nil
	}
	return resolveSandboxTools(ctx, optOut, nil, goarch, fetch)
}

// fileBuildFingerprint is the --file build cache key: builder.BuildFingerprint
// folded with the sandbox-tools digest via fingerprintWithTools.
func fileBuildFingerprint(containerfile []byte, baseRef string, agentBytes []byte, workspaceDir string, recipe cred.ToolRecipe, arch string, tools []toolcache.Fetched) (string, error) {
	fp, err := builder.BuildFingerprint(containerfile, baseRef, agentBytes, workspaceDir, recipe, arch)
	if err != nil {
		return "", err
	}
	return fingerprintWithTools(fp, tools), nil
}

// bootSandboxTools returns the tools to inject at boot via the OCI pull path:
// nil for --file (baked at build) and --rootfs (opaque ext4); otherwise
// resolveSandboxTools(ctx, f.noSandboxTools, nil, goarch, fetch).
func bootSandboxTools(ctx context.Context, f sandboxCreateFlags, goarch string, fetch toolFetchFn) []toolcache.Fetched {
	if f.filePath != "" || f.rootfsPath != "" {
		return nil
	}
	return resolveSandboxTools(ctx, f.noSandboxTools, nil, goarch, fetch)
}

// fingerprintWithTools folds the sandbox-tools digest into a build fingerprint.
// When toolcache.Digest(tools) is empty (no tools), fp is returned unchanged.
// Otherwise the return value is hex(sha256(fp + "\x00sandbox-tools\x00" + digest)).
func fingerprintWithTools(fp string, tools []toolcache.Fetched) string {
	digest := toolcache.Digest(tools)
	if digest == "" {
		return fp
	}
	h := sha256.New()
	h.Write([]byte(fp))
	h.Write([]byte("\x00sandbox-tools\x00"))
	h.Write([]byte(digest))
	return hex.EncodeToString(h.Sum(nil))
}
