package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// ResolvedUserMount is a mount resolved against a concrete host home directory.
// It is serialized into usermounts.json for the guest seed.
type ResolvedUserMount struct {
	HostPath         string `json:"host_path"`          // absolute host path
	GuestPath        string `json:"guest_path"`         // final in-guest path
	Overlay          bool   `json:"overlay"`            // true → guest seed must overlay StagingGuestPath onto GuestPath
	StagingGuestPath string `json:"staging_guest_path"` // live-mount landing point:
	// /run/nexus3/usermount/<basename> for Overlay=true rows,
	// == GuestPath for Overlay=false rows (direct mount, no staging needed).
}

// UserMountManifest is the schema of usermounts.json written into the
// agent-config staging dir. The guest seed reads it on first boot and:
//   - for Overlay=true rows: overlays StagingGuestPath (virtiofs) onto GuestPath
//   - for Overlay=false rows: the live mount is already at GuestPath; no action needed
//
// HostHome is included so the guest seed can create a /home/<user> → /root
// symlink when the operator's home is not /root (no-op if already /root).
type UserMountManifest struct {
	HostHome string              `json:"host_home"`
	Mounts   []ResolvedUserMount `json:"mounts"`
}

// BuildUserMountManifest resolves mounts (in "host:guest[:ro]" form) against
// hostHome and returns a UserMountManifest ready for virtiofs wiring and
// guest seeding.
//
//   - ~ and $HOME in the host path are expanded against hostHome.
//   - Mounts whose host path does not exist are silently skipped.
//   - Overlay is derived: guest paths under /root/.claude/ use overlay mode
//     (those dirs may contain pre-existing agent content that must be preserved);
//     all other guest paths mount directly (read-only).
//   - The :ro suffix is accepted but ignored for routing — all user mounts are
//     host-read-only by design (the virtiofs share is always RO).
//
// An empty or nil mounts slice returns a zero UserMountManifest (HostHome set,
// Mounts nil).
func BuildUserMountManifest(hostHome string, mounts []string) UserMountManifest {
	m := UserMountManifest{HostHome: hostHome}
	for _, spec := range mounts {
		// Parse "host:guest" or "host:guest:ro".
		// Split on the first colon for host, then take guest from the remainder
		// (ignoring a trailing :ro suffix — we derive RO from overlay logic).
		parts := strings.SplitN(spec, ":", 3)
		if len(parts) < 2 {
			continue // malformed — skip
		}
		hostRaw := parts[0]
		guestPath := parts[1]
		if hostRaw == "" || guestPath == "" {
			continue
		}

		// Expand ~ and $HOME in host path.
		hostPath := expandHome(hostRaw, hostHome)

		// Skip if host path does not exist.
		if _, err := os.Stat(hostPath); err != nil {
			continue
		}

		// Derive overlay: /root/.claude and paths under it need an overlay so
		// existing guest content (written by the agent) is not masked by the RO
		// share. Match the exact dir as well as descendants.
		overlay := guestPath == "/root/.claude" || strings.HasPrefix(guestPath, "/root/.claude/")

		stagingGuestPath := guestPath
		if overlay {
			// Land the virtiofs share at /run/nexus3/usermount/<basename>.
			base := filepath.Base(guestPath)
			if base == "" || base == "." || base == "/" {
				base = "um"
			}
			stagingGuestPath = "/run/nexus3/usermount/" + base
		}

		m.Mounts = append(m.Mounts, ResolvedUserMount{
			HostPath:         hostPath,
			GuestPath:        guestPath,
			Overlay:          overlay,
			StagingGuestPath: stagingGuestPath,
		})
	}
	return m
}

// expandHome replaces a leading ~ or $HOME with hostHome.
func expandHome(path, hostHome string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(hostHome, path[2:])
	}
	if path == "~" {
		return hostHome
	}
	if strings.HasPrefix(path, "$HOME/") {
		return filepath.Join(hostHome, path[6:])
	}
	if path == "$HOME" {
		return hostHome
	}
	return path
}

// CheckRecipeShadows returns one warning string for each mount spec in mounts
// whose guest path would shadow a path claimed by recipe: the agent binary
// path (BinPath), any package install directory, any declared symlink, or the
// parent directory of any of those paths (the PATH-entry directories the recipe
// installs into).
//
// Each returned string quotes the raw spec text so the caller can surface
// exactly which sandbox.mounts config line causes the conflict. Mounts are not
// filtered — warnings are advisory and the caller decides whether to log them
// or treat them as hard errors. Returns nil when recipe has no packages.
//
// This is the real call site for the shadow diagnostic (AC-5, D-TP-01).
func CheckRecipeShadows(mounts []string, recipe cred.ToolRecipe) []string {
	recipePaths := recipeGuestPaths(recipe)
	if len(recipePaths) == 0 {
		return nil
	}
	var warnings []string
	for _, spec := range mounts {
		guestPath := mountSpecGuestPath(spec)
		if guestPath == "" {
			continue
		}
		for _, rp := range recipePaths {
			if guestPathShadows(guestPath, rp) {
				warnings = append(warnings,
					fmt.Sprintf("user mount %q shadows recipe path %q; the guest binary may not resolve correctly", spec, rp))
				break
			}
		}
	}
	return warnings
}

// recipeGuestPaths collects all absolute guest paths declared by recipe,
// including the parent directories of each (the PATH-entry directories that
// contain the binaries and symlinks the recipe installs).
func recipeGuestPaths(recipe cred.ToolRecipe) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if p == "" || p == "." || p == "/" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	if recipe.BinPath != "" {
		add(recipe.BinPath)
		add(filepath.Dir(recipe.BinPath))
	}
	for _, pkg := range recipe.Packages {
		if d := recipeStableInstallDir(pkg.InstallDir); d != "" {
			add(d)
			if parent := filepath.Dir(d); parent != d {
				add(parent)
			}
		}
		for _, sym := range pkg.Symlinks {
			if sym.LinkPath != "" {
				add(sym.LinkPath)
				add(filepath.Dir(sym.LinkPath))
			}
		}
	}
	return paths
}

// recipeStableInstallDir returns the stable (template-free) prefix of an
// InstallDir by stripping any template placeholder (e.g. {VERSION}) and
// trailing slashes. An empty result means there is nothing to check.
func recipeStableInstallDir(dir string) string {
	if dir == "" {
		return ""
	}
	if i := strings.Index(dir, "{"); i >= 0 {
		dir = strings.TrimRight(dir[:i], "/")
	}
	return dir
}

// mountSpecGuestPath extracts the guest path from a "host:guest[:opts]" spec.
// Returns "" when the spec has no colon or an empty guest path.
func mountSpecGuestPath(spec string) string {
	i := strings.Index(spec, ":")
	if i < 0 {
		return ""
	}
	rest := spec[i+1:]
	// Strip trailing :ro or other option suffixes.
	if j := strings.Index(rest, ":"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// guestPathShadows reports whether a mount at mountPath shadows recipePath.
// Shadows: mountPath equals recipePath, or recipePath starts with mountPath+"/"
// (the mount covers the directory that contains the recipe artifact).
func guestPathShadows(mountPath, recipePath string) bool {
	return mountPath == recipePath || strings.HasPrefix(recipePath, mountPath+"/")
}

// WriteUserMountManifest writes m as usermounts.json into stageDir (mode 0o600).
// Mirrors the mcp-servers.json write in the A-MOUNT block of cmd_sandbox.go.
func WriteUserMountManifest(stageDir string, m UserMountManifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, "usermounts.json"), data, 0o600)
}
