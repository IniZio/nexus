package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// GuestCuratedPATHDirs: authoritative guest PATH dirs, staged/rebuilt by SeedGuestUserMounts (don't hardcode).
var GuestCuratedPATHDirs = []string{
	"/root/.local/bin",
	"/root/.bun/bin",
	"/root/.local/share/mise/shims",
}

// ResolvedUserMount is a mount resolved against a concrete host home directory.
type ResolvedUserMount struct {
	HostPath         string `json:"host_path"`
	GuestPath        string `json:"guest_path"`
	Overlay          bool   `json:"overlay"`
	Curated          bool   `json:"curated"`
	CuratedSubPath   string `json:"curated_sub_path,omitempty"`
	StagingGuestPath string `json:"staging_guest_path"` // virtiofs landing point
}

// UserMountManifest is the schema of usermounts.json for the guest seed.
type UserMountManifest struct {
	HostHome string              `json:"host_home"`
	Mounts   []ResolvedUserMount `json:"mounts"`
}

// BuildUserMountManifest resolves mounts (host:guest[:ro]) for virtiofs; expands ~ and $HOME; curated PATH off-PATH.
func BuildUserMountManifest(hostHome string, mounts []string) UserMountManifest {
	m := UserMountManifest{HostHome: hostHome}
	for _, spec := range mounts {
		parts := strings.SplitN(spec, ":", 3)
		if len(parts) < 2 {
			continue
		}
		hostRaw := parts[0]
		guestPath := parts[1]
		if hostRaw == "" || guestPath == "" {
			continue
		}

		hostPath := expandHome(hostRaw, hostHome)
		if _, err := os.Stat(hostPath); err != nil {
			continue
		}

		curated := false
		var curatedSubPath string
		for _, pd := range GuestCuratedPATHDirs {
			if guestPath == pd {
				curated = true
				break
			}
			if strings.HasPrefix(pd, guestPath+"/") {
				curated = true
				curatedSubPath = strings.TrimPrefix(pd, guestPath+"/")
				break
			}
		}

		overlay := false
		stagingGuestPath := guestPath
		switch {
		case curated:
			base := filepath.Base(guestPath)
			if base == "" || base == "." || base == "/" {
				base = "um"
			}
			stagingGuestPath = "/run/nexus3/usermount/bin-" + base
		case overlay:
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
			Curated:          curated,
			CuratedSubPath:   curatedSubPath,
			StagingGuestPath: stagingGuestPath,
		})
	}
	return m
}

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

// CheckRecipeShadows returns warnings for mount specs that shadow recipe paths (AC-5, D-TP-01).
func CheckRecipeShadows(mounts []string, recipe cred.ToolRecipe) []string {
	recipePaths := recipeGuestPaths(recipe)
	if len(recipePaths) == 0 {
		return nil
	}
	var warnings []string
	for _, spec := range mounts {
		guestPath := MountSpecGuestPath(spec)
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

func recipeStableInstallDir(dir string) string {
	if dir == "" {
		return ""
	}
	if i := strings.Index(dir, "{"); i >= 0 {
		dir = strings.TrimRight(dir[:i], "/")
	}
	return dir
}

// MountSpecGuestPath extracts the guest path from a "host:guest[:opts]" spec.
func MountSpecGuestPath(spec string) string {
	i := strings.Index(spec, ":")
	if i < 0 {
		return ""
	}
	rest := spec[i+1:]
	if j := strings.Index(rest, ":"); j >= 0 {
		return rest[:j]
	}
	return rest
}

func guestPathShadows(mountPath, recipePath string) bool {
	return mountPath == recipePath || strings.HasPrefix(recipePath, mountPath+"/")
}

// WriteUserMountManifest writes m as usermounts.json into stageDir (mode 0o600).
func WriteUserMountManifest(stageDir string, m UserMountManifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, "usermounts.json"), data, 0o600)
}
