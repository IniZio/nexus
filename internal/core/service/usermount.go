package service

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// UserMount declares one operator-owned host directory that an agent sandbox
// should receive as a read-only live mount. Extend by appending rows to
// userMountTable; NEVER add secret dirs (~/.ssh, ~/.config/gh, ~/.claude.json,
// ~/.claude/.credentials.json) — those are deliberately excluded so the
// security boundary holds by omission.
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

// userMountTable is the canonical ordered list of operator tool dirs to share
// into agent sandboxes. Deterministic order → deterministic guest layout.
// Extend by appending rows. Do NOT add secret dirs — see UserMount doc comment.
var userMountTable = []UserMount{
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

// ResolvedUserMount is a userMountTable row resolved against a concrete host
// home directory. It is serialized into usermounts.json for the guest seed.
type ResolvedUserMount struct {
	HostPath         string `json:"host_path"`          // absolute host path
	GuestPath        string `json:"guest_path"`         // final in-guest path
	Overlay          bool   `json:"overlay"`            // true → guest seed must overlay StagingGuestPath onto GuestPath
	StagingGuestPath string `json:"staging_guest_path"` // live-mount landing point:
	// /run/nexus3/usermount/<basename> for Overlay=true rows,
	// == GuestPath for Overlay=false rows (direct mount, no staging needed).
}

// ResolveUserMounts expands userMountTable against hostHome and returns only
// rows whose host directory exists on disk. Missing dirs are silently skipped
// (no broken mounts). Order matches the table (deterministic).
func ResolveUserMounts(hostHome string) []ResolvedUserMount {
	var out []ResolvedUserMount
	for _, m := range userMountTable {
		abs := filepath.Join(hostHome, m.HostRel)
		if _, err := os.Stat(abs); err != nil {
			continue // dir absent or unreadable → skip
		}
		stagingGuestPath := m.GuestPath
		if m.Overlay {
			// Land the virtiofs share at a staging path under /run/nexus3/usermount/
			// so the guest seed can overlay it onto GuestPath without conflicting
			// with any pre-existing content there. Mirrors the agentcfg-lower pattern.
			stagingGuestPath = "/run/nexus3/usermount/" + filepath.Base(m.HostRel)
		}
		out = append(out, ResolvedUserMount{
			HostPath:         abs,
			GuestPath:        m.GuestPath,
			Overlay:          m.Overlay,
			StagingGuestPath: stagingGuestPath,
		})
	}
	return out
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

// WriteUserMountManifest writes m as usermounts.json into stageDir (mode 0o600).
// Mirrors the mcp-servers.json write in the A-MOUNT block of cmd_sandbox.go.
func WriteUserMountManifest(stageDir string, m UserMountManifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, "usermounts.json"), data, 0o600)
}
