package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResolvePluginSymlinkMounts scans <hostHome>/.claude/plugins: every symlink
// directly under it, and every symlink one level below (i.e. inside each
// immediate subdirectory), whose resolved target lies outside <hostHome>/.claude
// yields a read-only mount spec "<target>:<target>:ro" so the link resolves
// inside a guest that live-mounts ~/.claude. Targets are deduplicated (exact
// duplicates and targets nested under an already-included target are dropped)
// and returned sorted. Dangling or unresolvable links produce a warning string
// and are skipped. A missing plugins dir returns (nil, nil).
func ResolvePluginSymlinkMounts(hostHome string) (specs []string, warnings []string) {
	claudeDir := filepath.Join(hostHome, ".claude")
	pluginsDir := filepath.Join(claudeDir, "plugins")

	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil, nil
	}

	realClaude := claudeDir
	if r, err := filepath.EvalSymlinks(claudeDir); err == nil {
		realClaude = r
	}
	inside := func(target string) bool {
		for _, root := range []string{claudeDir, realClaude} {
			if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}

	var targets []string
	consider := func(linkPath string) {
		target, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin symlink %s: %v; skipped", linkPath, err))
			return
		}
		if inside(target) {
			return
		}
		targets = append(targets, target)
	}

	for _, d := range entries {
		p := filepath.Join(pluginsDir, d.Name())
		if d.Type()&os.ModeSymlink != 0 {
			consider(p)
			continue
		}
		if !d.IsDir() {
			continue
		}
		sub, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, s := range sub {
			if s.Type()&os.ModeSymlink != 0 {
				consider(filepath.Join(p, s.Name()))
			}
		}
	}

	sort.Strings(targets)
	var kept []string
	for _, t := range targets {
		if n := len(kept); n > 0 {
			last := kept[n-1]
			if t == last || strings.HasPrefix(t, last+string(filepath.Separator)) {
				continue
			}
		}
		kept = append(kept, t)
	}
	for _, t := range kept {
		specs = append(specs, t+":"+t+":ro")
	}
	return specs, warnings
}
