package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResolvePluginSymlinkMounts returns "<host>:<guest>:ro" specs for plugin
// directories outside <hostHome>/.claude. Three sources: (1) symlinks ≤2
// levels under ~/.claude/plugins — spec is real:real:ro; (2) installLocation
// and source.path from known_marketplaces.json — spec is real:original:ro so
// claude finds the path at its recorded installLocation even when that path is
// a symlink to a different real directory; (3) installPath entries from
// installed_plugins.json. Deduped by guest path, nesting-collapsed, sorted.
// Missing JSON → warning, not error. Absent host paths → warning, not spec.
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

	type entry struct {
		host  string
		guest string
	}
	var mounts []entry
	guestSeen := map[string]bool{}

	considerSymlink := func(linkPath string) {
		target, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin symlink %s: %v; skipped", linkPath, err))
			return
		}
		if inside(target) || guestSeen[target] {
			return
		}
		mounts = append(mounts, entry{host: target, guest: target})
		guestSeen[target] = true
	}

	considerRawPath := func(p string) {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin path %s: does not exist on this host; skipped", p))
			return
		}
		if inside(real) || guestSeen[p] {
			return
		}
		mounts = append(mounts, entry{host: real, guest: p})
		guestSeen[p] = true
	}

	for _, d := range entries {
		p := filepath.Join(pluginsDir, d.Name())
		if d.Type()&os.ModeSymlink != 0 {
			considerSymlink(p)
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
				considerSymlink(filepath.Join(p, s.Name()))
			}
		}
	}

	mwPaths, mwWarns := readMarketplacePaths(pluginsDir)
	warnings = append(warnings, mwWarns...)
	for _, p := range mwPaths {
		considerRawPath(p)
	}

	ipPaths, ipWarns := readInstalledPluginPaths(pluginsDir)
	warnings = append(warnings, ipWarns...)
	for _, p := range ipPaths {
		considerRawPath(p)
	}

	sort.Slice(mounts, func(i, j int) bool { return mounts[i].guest < mounts[j].guest })
	var kept []entry
	for _, m := range mounts {
		if n := len(kept); n > 0 {
			last := kept[n-1]
			if m.guest == last.guest || strings.HasPrefix(m.guest, last.guest+string(filepath.Separator)) {
				continue
			}
		}
		kept = append(kept, m)
	}
	for _, m := range kept {
		specs = append(specs, m.host+":"+m.guest+":ro")
	}
	return specs, warnings
}

func readMarketplacePaths(pluginsDir string) (paths []string, warnings []string) {
	data, err := os.ReadFile(filepath.Join(pluginsDir, "known_marketplaces.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("known_marketplaces.json: read error: %v; skipped", err))
		}
		return nil, warnings
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		warnings = append(warnings, fmt.Sprintf("known_marketplaces.json: parse error: %v; skipped", err))
		return nil, warnings
	}
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if loc, ok := m["installLocation"].(string); ok && loc != "" {
			paths = append(paths, loc)
		}
		if src, ok := m["source"].(map[string]any); ok {
			if p, ok := src["path"].(string); ok && p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths, warnings
}

func readInstalledPluginPaths(pluginsDir string) (paths []string, warnings []string) {
	data, err := os.ReadFile(filepath.Join(pluginsDir, "installed_plugins.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("installed_plugins.json: read error: %v; skipped", err))
		}
		return nil, warnings
	}
	var top struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		warnings = append(warnings, fmt.Sprintf("installed_plugins.json: parse error: %v; skipped", err))
		return nil, warnings
	}
	for _, raw := range top.Plugins {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err == nil {
			if p, ok := obj["installPath"].(string); ok && p != "" {
				paths = append(paths, p)
			}
			continue
		}
		var arr []map[string]any
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, item := range arr {
				if p, ok := item["installPath"].(string); ok && p != "" {
					paths = append(paths, p)
				}
			}
		}
	}
	return paths, warnings
}
