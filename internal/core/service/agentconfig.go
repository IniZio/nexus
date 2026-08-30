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

// AssembleCuratedConfig stages a curated, secret-free subset of the agent's
// config directory into destDir. Only files matching profile.MountAllowlist are
// considered, and even those are subject to hard exclusions (see secretFileNames
// and the profile's settings-file filter). destDir is created if absent.
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
		if err := copyGlob(src, pattern, destDir, profile); err != nil {
			return err
		}
	}
	// Guarantee the bypass-consent key (when this agent has one) is present in
	// the lower overlayfs layer even when the host has no settings file.
	// Without this, a fresh host with no settings file produces a lower layer
	// with no settings file at all; an upper-layer single-key write would then
	// still be needed — but once the lower DOES have a settings file (e.g.
	// enabledPlugins present), an upper-layer write shadows the whole file and
	// silently drops it. Ensuring the key is in the lower layer lets the
	// supervisor skip the upper write entirely when sharing is on.
	//
	// Agents with no BypassConsentKey (e.g. cursor, whose skip-permissions
	// posture is a launch-time flag) skip this step entirely.
	if profile.BypassConsentKey == "" {
		return nil
	}
	return ensureStagedBypassConsentKey(destDir, profile)
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

// settingsBaseName returns the basename of profile.SettingsPath — the
// filename, within the agent's config directory, that carries structured
// settings subject to the profile's SettingsAllowlist filter (e.g.
// "settings.json" for Claude Code, "cli-config.json" for cursor). Empty when
// the profile has no settings file.
func settingsBaseName(profile cred.AgentProfile) string {
	if profile.SettingsPath == "" {
		return ""
	}
	return filepath.Base(profile.SettingsPath)
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
func copyGlob(srcDir, pattern, destDir string, profile cred.AgentProfile) error {
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

		return walkFollowDirs(srcDir, walkRoot, destDir, map[string]bool{}, profile)
	}

	// Non-recursive: single-level match. Lstat (does NOT follow) so a symlinked
	// settings/CLAUDE.md file is skipped rather than followed to its target.
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
	return copyFile(srcPath, pattern, destDir, profile)
}

// walkFollowDirs walks dir recursively, FOLLOWING directory symlinks (with a
// resolved-realpath visited set to break cycles) and copying only regular
// files. File symlinks and non-regular entries are skipped. srcRoot anchors the
// relative layout so the guest sees paths under the symlink NAME, not its
// target. See copyGlob's symlink policy.
func walkFollowDirs(srcRoot, dir, destDir string, visited map[string]bool, profile cred.AgentProfile) error {
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
				if err := walkFollowDirs(srcRoot, full, destDir, visited, profile); err != nil {
					return err
				}
			}
			// symlink to a file → skip.
		case mode.IsDir():
			if err := walkFollowDirs(srcRoot, full, destDir, visited, profile); err != nil {
				return err
			}
		case mode.IsRegular():
			rel, err := filepath.Rel(srcRoot, full)
			if err != nil {
				return err
			}
			if err := copyFile(full, rel, destDir, profile); err != nil {
				return err
			}
			// other (device/socket/etc.) → skip.
		}
	}
	return nil
}

// copyFile copies one source file to destDir/relPath, subject to hard
// exclusions and the profile's settings-file filter. Existing destination
// files are overwritten.
func copyFile(srcPath, relPath, destDir string, profile cred.AgentProfile) error {
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

	// The profile's settings file gets filtered; everything else is a straight copy.
	if settingsBase := settingsBaseName(profile); settingsBase != "" && base == settingsBase {
		return copyFilteredSettings(srcPath, dstPath, profile)
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

// copyFilteredSettings reads srcPath as JSON and writes only the keys
// allowlisted by profile.SettingsAllowlist to dstPath at mode 0444. Any key
// not in the allowlist — including unrecognised/future keys, and including a
// credential blob like cursor's authInfo — is dropped, so a secret can never
// ride in on a key this profile has not vetted. A nil/empty allowlist drops
// every key (see [cred.AgentProfile.SettingsAllowlist]'s zero-value doc).
//
// When profile.BypassConsentKey is set, that key is always forced to true in
// the output regardless of the host value, so the staged lower overlayfs
// layer always carries the bypass key alongside whatever else survived
// filtering. (An upper-layer write of a single-key file would shadow the
// entire lower file, silently dropping the rest — see the overlay
// file-granularity caveat on ensureStagedBypassConsentKey.) Agents with no
// BypassConsentKey (e.g. cursor) skip this step.
func copyFilteredSettings(src, dst string, profile cred.AgentProfile) error {
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
		if !profile.SettingsAllowlist[key] {
			delete(raw, key)
		}
	}
	if profile.BypassConsentKey != "" {
		// Always force the bypass key so the lower layer needs no upper shadow.
		raw[profile.BypassConsentKey] = json.RawMessage("true")
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o444)
}

// ensureStagedBypassConsentKey guarantees that destDir/<settings file> exists
// and carries profile.BypassConsentKey:true. Called by AssembleCuratedConfig
// after the copyGlob pass so the bypass key is present even when the host has
// no settings file (in which case copyFilteredSettings is never invoked and
// the file would otherwise be absent from the lower layer). Callers must not
// invoke this for a profile with an empty BypassConsentKey or settings
// basename; AssembleCuratedConfig guards both.
//
// The file may have been written at mode 0o444 by copyFilteredSettings; we
// write via a sibling temp file + rename to avoid a permission-denied overwrite.
func ensureStagedBypassConsentKey(destDir string, profile cred.AgentProfile) error {
	settingsBase := settingsBaseName(profile)
	if settingsBase == "" {
		return nil
	}
	p := filepath.Join(destDir, settingsBase)
	var raw map[string]json.RawMessage
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			raw = nil
		}
	case os.IsNotExist(err):
		// No settings file staged yet — start from empty object.
	default:
		return err
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}
	raw[profile.BypassConsentKey] = json.RawMessage("true")
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
