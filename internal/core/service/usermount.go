package service

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// UserMount declares one operator-owned host directory that an agent sandbox
// should receive as a read-only live mount. Extend by appending rows to
// defaultUserMounts; NEVER add secret dirs (~/.ssh, ~/.config/gh,
// ~/.claude.json, ~/.claude/.credentials.json) — those are deliberately
// excluded so the security boundary holds by omission.
//
// HostRel is relative to the operator's $HOME.
// GuestPath is the final in-guest path the agent expects the content at.
// Overlay=true means the host dir is NOT mounted directly at GuestPath; instead
// the live mount lands at a staging path (/run/nexus3/usermount/<basename>) and
// the guest seed overlays it onto GuestPath. This is required when GuestPath may
// already contain content the agent has written. Overlay=false mounts directly
// at GuestPath (read-only), which is safe when GuestPath is purely tool content
// the guest never mutates.
type UserMount struct {
	HostRel   string // path relative to $HOME
	GuestPath string // final in-guest path
	Overlay   bool   // true → stage at /run/nexus3/usermount/<basename>; false → mount directly at GuestPath
}

// defaultUserMounts is the built-in ordered list of generic operator tool dirs
// to share into agent sandboxes. These are chosen because they are common to
// most developer machines; they carry no credentials. Users may add
// machine-specific entries via ~/.config/nexus3/config.yaml (agent_mounts:
// mounts:). Deterministic order → deterministic guest layout.
//
// Do NOT add secret dirs — see UserMount doc comment.
var defaultUserMounts = []UserMount{
	{HostRel: ".claude/plugins", GuestPath: "/root/.claude/plugins", Overlay: true},
	{HostRel: ".local/bin", GuestPath: "/root/.local/bin", Overlay: false},
	{HostRel: ".local/share/groundwork", GuestPath: "/root/.local/share/groundwork", Overlay: false},

	// Version-store dirs: ~/.local/bin entries are frequently thin symlinks or
	// wrapper scripts that resolve into a version-manager's install tree (mise,
	// bun, codegraph, uv). Mounting only ~/.local/bin leaves those links
	// dangling in the guest, so the tool "is not found" even though the symlink
	// is present. These rows carry the actual install trees the operator's MCP
	// servers and CLIs resolve into. Read-only; runtime caches (~/.cache/uv,
	// etc.) stay guest-local and writable. NOT a security widening: these are
	// tool payloads, never credential stores.
	{HostRel: ".codegraph", GuestPath: "/root/.codegraph", Overlay: false},
	{HostRel: ".local/share/mise", GuestPath: "/root/.local/share/mise", Overlay: false}, // node + uv binaries
	{HostRel: ".local/share/uv", GuestPath: "/root/.local/share/uv", Overlay: false},     // uv-managed python
	{HostRel: ".bun", GuestPath: "/root/.bun", Overlay: false},                           // bun global (agent-browser)
	// Only the extensions subtree — NOT all of ~/.vscode-server, which holds VS
	// Code user state and may carry auth tokens (secret boundary).
	{HostRel: ".vscode-server/extensions", GuestPath: "/root/.vscode-server/extensions", Overlay: false},
}

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

// ResolvedUserSymlink is a guest-side symlink declaration from user config.
// The guest seed creates these symlinks on first boot (entirely in-guest; no
// host path is involved). Built-in defaults contribute no symlinks.
type ResolvedUserSymlink struct {
	Link   string `json:"link"`   // guest path to create (the symlink)
	Target string `json:"target"` // guest path it points to
}

// ResolveUserMounts merges the built-in defaultUserMounts with any
// user-configured mounts from ~/.config/nexus3/config.yaml, resolves them
// against hostHome, and returns:
//   - mounts: only rows whose host directory exists on disk (missing dirs
//     silently skipped — no broken mounts). Order is deterministic: defaults
//     first (unless disable_defaults), then user entries.
//   - symlinks: guest-side symlinks declared in user config (not filtered by
//     host existence — their targets are guest paths).
//
// On config load error, slog.Warn is emitted and defaults-only is used; sandbox
// create is never failed for a config parse error.
func ResolveUserMounts(hostHome string) ([]ResolvedUserMount, []ResolvedUserSymlink) {
	cfg, err := LoadUserMountConfig(hostHome)
	if err != nil {
		slog.Warn("usermount: failed to load user config; using built-in defaults only", "err", err)
		cfg = UserMountConfig{}
	}

	// Build a set of guest paths provided by user config so defaults with the
	// same GuestPath can be skipped (user entry overrides the default).
	userByGuest := make(map[string]bool, len(cfg.Mounts))
	for _, u := range cfg.Mounts {
		userByGuest[u.Guest] = true
	}

	// Collect candidates: defaults (filtered by override set) then user entries.
	type candidate struct {
		hostPath  string
		guestPath string
		overlay   bool
	}
	var candidates []candidate

	if !cfg.DisableDefaults {
		for _, d := range defaultUserMounts {
			if userByGuest[d.GuestPath] {
				continue // user entry takes precedence
			}
			candidates = append(candidates, candidate{
				hostPath:  filepath.Join(hostHome, d.HostRel),
				guestPath: d.GuestPath,
				overlay:   d.Overlay,
			})
		}
	}
	for _, u := range cfg.Mounts {
		candidates = append(candidates, candidate{
			hostPath:  u.Host, // already expanded by LoadUserMountConfig
			guestPath: u.Guest,
			overlay:   u.Overlay,
		})
	}

	// Resolve: skip candidates whose host path is absent.
	var mounts []ResolvedUserMount
	for _, c := range candidates {
		if _, err := os.Stat(c.hostPath); err != nil {
			continue // dir absent or unreadable → skip
		}
		stagingGuestPath := c.guestPath
		if c.overlay {
			// Land the virtiofs share under /run/nexus3/usermount/ so the guest
			// seed can overlay it onto GuestPath without conflicting with any
			// pre-existing content there.
			stagingGuestPath = "/run/nexus3/usermount/" + filepath.Base(c.hostPath)
		}
		mounts = append(mounts, ResolvedUserMount{
			HostPath:         c.hostPath,
			GuestPath:        c.guestPath,
			Overlay:          c.overlay,
			StagingGuestPath: stagingGuestPath,
		})
	}

	// Symlinks: from config only; defaults contribute none.
	var symlinks []ResolvedUserSymlink
	for _, s := range cfg.Symlinks {
		symlinks = append(symlinks, ResolvedUserSymlink{Link: s.Link, Target: s.Target})
	}

	return mounts, symlinks
}

// UserMountManifest is the schema of usermounts.json written into the
// agent-config staging dir. The guest seed reads it on first boot and:
//   - for Overlay=true rows: overlays StagingGuestPath (virtiofs) onto GuestPath
//   - for Overlay=false rows: the live mount is already at GuestPath; no action needed
//   - Symlinks: creates each declared guest-side symlink
//
// HostHome is included so the guest seed can create a /home/<user> → /root
// symlink when the operator's home is not /root (no-op if already /root).
type UserMountManifest struct {
	HostHome string                `json:"host_home"`
	Mounts   []ResolvedUserMount   `json:"mounts"`
	Symlinks []ResolvedUserSymlink `json:"symlinks,omitempty"`
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
