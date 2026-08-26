package service

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// secretFileNames is the hard exclusion list. These filenames are NEVER copied
// into destDir regardless of what MountAllowlist says. Deny wins over allow.
var secretFileNames = map[string]bool{
	".credentials.json":   true,
	".claude.json":        true,
	"settings.local.json": true,
}

// portableSettingsTopKeys is the ALLOWLIST of settings.json top-level keys that
// are safe to share into a guest. This is a deliberate allowlist, not a
// denylist: a settings key we have never seen — including a FUTURE Claude Code
// key that carries a secret — is dropped by default rather than leaked. Adding
// a key here is an explicit decision that it is non-secret and portable.
//
// Deliberately EXCLUDED (dropped): apiKeyHelper/awsCredentialExport/
// gcpAuthRefresh/otelHeadersHelper (credential helpers), env (may hold secrets),
// hooks (reference host filesystem paths), permissions (host-specific
// allowlists), sandbox (its credentials subtree is secret and its network
// policy is irrelevant inside a nexus3 perimeter), and anything unrecognised.
var portableSettingsTopKeys = map[string]bool{
	"model":                true,
	"advisorModel":         true,
	"availableModels":      true,
	"theme":                true,
	"tui":                  true,
	"defaultShell":         true,
	"attribution":          true,
	"enabledPlugins":       true,
	"extraKnownMarketplaces": true,
	"autoMode":             true,
	"effortLevel":          true,
	"autoUpdatesChannel":   true,
	"enableWorkflows":      true,
	// Bypass-permissions consent: always carried into the lower overlayfs layer
	// so plugins (enabledPlugins) and this key coexist. The overlay is
	// file-granular: an upper-layer write of a single-key settings.json would
	// shadow the entire lower file, dropping enabledPlugins and
	// extraKnownMarketplaces. Carrying the key here keeps them all in one layer.
	"skipDangerousModePermissionPrompt": true,
}

// AssembleCuratedConfig stages a curated, secret-free subset of the agent's
// config directory into destDir. Only files matching profile.MountAllowlist are
// considered, and even those are subject to hard exclusions (see secretFileNames
// and the settings.json filter). destDir is created if absent.
//
// agentConfigDir is the already-resolved source directory (tilde already
// expanded by the caller, or pass the raw path and let this function resolve
// it — resolution is idempotent).
//
// Missing source entries are skipped silently. Returns an error only for real
// IO/permission failures.
func AssembleCuratedConfig(profile cred.AgentProfile, agentConfigDir string, destDir string) error {
	// Resolve leading "~/" in agentConfigDir in case the caller passed a raw path.
	src, err := expandTilde(agentConfigDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	for _, pattern := range profile.MountAllowlist {
		if err := copyGlob(src, pattern, destDir); err != nil {
			return err
		}
	}
	// Guarantee the bypass-consent key is present in the lower overlayfs layer
	// even when the host has no settings.json. Without this, a fresh host with no
	// settings.json produces a lower layer with no settings.json at all; the
	// supervisor's upper-layer single-key write would then still be needed — but
	// once the lower DOES have a settings.json (e.g. enabledPlugins present), an
	// upper-layer write shadows the whole file and silently drops enabledPlugins.
	// Ensuring the key is in the lower layer lets the supervisor skip the upper
	// write entirely when sharing is on.
	return ensureStagedBypassConsentKey(destDir)
}

// expandTilde expands a leading "~/" to the user's home directory.
func expandTilde(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, p[2:]), nil
}

// copyGlob copies all files under srcDir that match the glob pattern into
// destDir, preserving relative layout.
//
// A pattern containing "**" is treated as a recursive match: everything at or
// below the prefix is walked. A plain pattern (no "**") is matched against the
// basename of files at the appropriate depth.
//
// Symlink policy (SECURITY — the curated tree must never leak a secret):
//   - Directory symlinks are FOLLOWED. This is required for real config trees,
//     where skills are commonly symlinks to dirs living outside the config dir
//     (e.g. ~/.claude/skills/foo -> ~/.agents/skills/foo). Regular files found
//     under a followed dir are still subject to the basename deny.
//   - File symlinks are NEVER read or copied. A file that is itself a symlink
//     under the shared tree is the exfil vector (skills/notes.md ->
//     ~/.claude/.credentials.json, or -> an arbitrarily-named out-of-tree
//     secret); refusing all file symlinks closes it completely without having
//     to reason about the target's name or location. Legit skills carry
//     regular files, so the cost is only exotic per-file symlinks.
//   - Only regular files are ever opened (copyFile is called on nothing else).
func copyGlob(srcDir, pattern, destDir string) error {
	if strings.Contains(pattern, "**") {
		// Recursive: walk everything under the prefix before "**".
		parts := strings.SplitN(pattern, "**", 2)
		prefix := strings.TrimSuffix(parts[0], "/")
		walkRoot := filepath.Join(srcDir, prefix)

		// os.Stat follows a symlinked prefix dir (e.g. a symlinked skills/).
		info, err := os.Stat(walkRoot)
		if os.IsNotExist(err) {
			return nil // missing source is fine
		}
		if err != nil {
			return nil // broken symlink or unreadable — degrade to no-share for this glob
		}
		if !info.IsDir() {
			return nil
		}

		return walkFollowDirs(srcDir, walkRoot, destDir, map[string]bool{})
	}

	// Non-recursive: single-level match. Lstat (does NOT follow) so a symlinked
	// CLAUDE.md/settings.json is skipped rather than followed to its target.
	srcPath := filepath.Join(srcDir, pattern)
	fi, err := os.Lstat(srcPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil // dir, symlink, device, etc. — never copy
	}
	return copyFile(srcPath, pattern, destDir)
}

// walkFollowDirs walks dir recursively, FOLLOWING directory symlinks (with a
// resolved-realpath visited set to break cycles) and copying only regular
// files. File symlinks and non-regular entries are skipped. srcRoot anchors the
// relative layout so the guest sees paths under the symlink NAME, not its
// target. See copyGlob's symlink policy.
func walkFollowDirs(srcRoot, dir, destDir string, visited map[string]bool) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil // broken/unreadable — skip this subtree, do not abort the share
	}
	if visited[real] {
		return nil // symlink cycle guard
	}
	visited[real] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // unreadable subtree — skip rather than abort
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".git" {
			continue
		}
		full := filepath.Join(dir, name)
		fi, err := os.Lstat(full)
		if err != nil {
			continue
		}
		mode := fi.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			// Resolve one level to learn dir-vs-file. Dir symlinks are followed;
			// file symlinks are never read.
			ti, terr := os.Stat(full)
			if terr != nil {
				continue // broken symlink
			}
			if ti.IsDir() {
				if err := walkFollowDirs(srcRoot, full, destDir, visited); err != nil {
					return err
				}
			}
			// symlink to a file → skip.
		case mode.IsDir():
			if err := walkFollowDirs(srcRoot, full, destDir, visited); err != nil {
				return err
			}
		case mode.IsRegular():
			rel, err := filepath.Rel(srcRoot, full)
			if err != nil {
				return err
			}
			if err := copyFile(full, rel, destDir); err != nil {
				return err
			}
			// other (device/socket/etc.) → skip.
		}
	}
	return nil
}

// copyFile copies one source file to destDir/relPath, subject to hard
// exclusions and the settings.json filter. Existing destination files are
// overwritten.
func copyFile(srcPath, relPath, destDir string) error {
	base := filepath.Base(relPath)

	// Hard exclusion 1: secret filenames.
	if secretFileNames[base] {
		return nil
	}

	// Hard exclusion 2: anything inside a .git directory.
	for _, seg := range strings.Split(filepath.ToSlash(relPath), "/") {
		if seg == ".git" {
			return nil
		}
	}

	// Hard exclusion 3 (defense-in-depth): only ever open a REGULAR file. The
	// callers already filter to regular files, but a symlink reaching here would
	// otherwise be followed by ReadFile and could exfiltrate a secret target.
	if fi, err := os.Lstat(srcPath); err != nil || !fi.Mode().IsRegular() {
		return nil
	}

	dstPath := filepath.Join(destDir, relPath)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}

	// settings.json gets filtered; everything else is a straight copy.
	if base == "settings.json" {
		return copyFilteredSettings(srcPath, dstPath)
	}
	return copyRaw(srcPath, dstPath)
}

// copyRaw copies a file verbatim at mode 0444.
func copyRaw(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o444)
}

// copyFilteredSettings reads srcPath as JSON and writes only the ALLOWLISTED
// portable top-level keys to dstPath at mode 0444. Any key not in
// portableSettingsTopKeys — including unrecognised/future keys — is dropped, so
// a secret can never ride in on a key we have not vetted.
//
// skipDangerousModePermissionPrompt is always forced to true in the output
// regardless of the host value, so the staged lower overlayfs layer always
// carries the bypass key alongside enabledPlugins and extraKnownMarketplaces.
// (An upper-layer write of a single-key file would shadow the entire lower
// file, silently dropping plugins — see the overlay file-granularity caveat.)
func copyFilteredSettings(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Not valid JSON — skip silently rather than propagating a parse error.
		return nil
	}

	for key := range raw {
		if !portableSettingsTopKeys[key] {
			delete(raw, key)
		}
	}
	// Always force the bypass key so the lower layer needs no upper shadow.
	raw["skipDangerousModePermissionPrompt"] = json.RawMessage("true")

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o444)
}

// ensureStagedBypassConsentKey guarantees that destDir/settings.json exists
// and carries skipDangerousModePermissionPrompt:true. Called by
// AssembleCuratedConfig after the copyGlob pass so the bypass key is present
// even when the host has no settings.json (in which case copyFilteredSettings
// is never invoked and the file would otherwise be absent from the lower layer).
//
// The file may have been written at mode 0o444 by copyFilteredSettings; we
// write via a sibling temp file + rename to avoid a permission-denied overwrite.
func ensureStagedBypassConsentKey(destDir string) error {
	p := filepath.Join(destDir, "settings.json")
	var raw map[string]json.RawMessage
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			raw = nil
		}
	case os.IsNotExist(err):
		// No settings.json staged yet — start from empty object.
	default:
		return err
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}
	raw["skipDangerousModePermissionPrompt"] = json.RawMessage("true")
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	// Write via temp-file rename so we can replace a read-only file (0o444)
	// without needing to chmod it first. The rename atomically replaces the
	// existing file; the new file is created writable and the caller's process
	// umask applies to the temp, but we set the final mode explicitly via
	// os.Chmod after rename.
	tmp := p + ".nexus3tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(p, 0o444)
}
