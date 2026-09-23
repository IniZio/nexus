package cred

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

//go:embed profiles/*.json
var builtinProfileFS embed.FS

var profilesOnce sync.Once

func ensureProfiles() {
	profilesOnce.Do(func() {
		loaded, err := loadProfilesFromFS(builtinProfileFS, "profiles")
		if err != nil {
			panic(err.Error())
		}
		for k, v := range loaded {
			profiles[k] = v
		}
		for k, v := range loadProfilesFromDir(userProfileDir()) {
			profiles[k] = v
		}
	})
}

func userProfileDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "nexus", "profiles")
}

func loadProfilesFromFS(fsys fs.FS, dir string) (map[string]AgentProfile, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("cred: loadProfilesFromFS: read dir %q: %w", dir, err)
	}
	out := make(map[string]AgentProfile, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("cred: loadProfilesFromFS: read %q: %w", e.Name(), err)
		}
		p, err := decodeProfile(data, e.Name())
		if err != nil {
			return nil, err
		}
		out[p.Name] = p
	}
	return out, nil
}

// loadProfilesFromDir reads *.json files from dir. A non-existent dir is
// silently ignored. A malformed document is logged at Warn and skipped;
// the built-in profile with the same name is left unchanged.
func loadProfilesFromDir(dir string) map[string]AgentProfile {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		slog.Warn("cred: user profile dir unreadable", "dir", dir, "err", err)
		return nil
	}
	out := make(map[string]AgentProfile)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("cred: user profile unreadable; skipping", "path", path, "err", err)
			continue
		}
		p, err := decodeProfile(data, e.Name())
		if err != nil {
			slog.Warn("cred: user profile malformed; skipping", "path", path, "err", err)
			continue
		}
		out[p.Name] = p
	}
	return out
}

// decodeProfile decodes one JSON profile document. Unknown fields and missing
// Name are hard errors so a malformed document fails loud at load time.
func decodeProfile(data []byte, docName string) (AgentProfile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p AgentProfile
	if err := dec.Decode(&p); err != nil {
		return AgentProfile{}, fmt.Errorf("cred: profile %q: %w", docName, err)
	}
	if p.Name == "" {
		return AgentProfile{}, fmt.Errorf("cred: profile %q: Name is required", docName)
	}
	if len(p.SettingsAllowlist) > 0 && len(p.SettingsDenylist) > 0 {
		return AgentProfile{}, fmt.Errorf("cred: profile %q: SettingsAllowlist and SettingsDenylist are mutually exclusive", docName)
	}
	return p, nil
}
