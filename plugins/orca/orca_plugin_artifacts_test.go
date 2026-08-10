package orcaplugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// repoRoot walks up from the test file's directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod not found)")
		}
		dir = parent
	}
}

func pluginDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "plugins", "orca")
}

// --- manifest ---

type manifest struct {
	ManifestVersion int    `json:"manifestVersion"`
	ID              string `json:"id"`
	Publisher       string `json:"publisher"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Description     string `json:"description"`
	Engines         struct {
		Orca string `json:"orca"`
	} `json:"engines"`
	PluginAPI   int `json:"pluginApi"`
	Contributes struct {
		VMRecipes []struct {
			Path string `json:"path"`
		} `json:"vmRecipes"`
	} `json:"contributes"`
}

func TestManifest(t *testing.T) {
	pd := pluginDir(t)
	data, err := os.ReadFile(filepath.Join(pd, "orca-plugin.json"))
	if err != nil {
		t.Fatalf("read orca-plugin.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse orca-plugin.json: %v", err)
	}

	if m.ManifestVersion != 1 {
		t.Errorf("manifestVersion: want 1, got %d", m.ManifestVersion)
	}
	if m.PluginAPI != 1 {
		t.Errorf("pluginApi: want 1, got %d", m.PluginAPI)
	}
	if m.ID == "" {
		t.Error("id must be non-empty")
	}
	if m.Publisher == "" {
		t.Error("publisher must be non-empty")
	}
	if m.Name == "" {
		t.Error("name must be non-empty")
	}

	semver := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	if !semver.MatchString(m.Version) {
		t.Errorf("version %q is not semver (want X.Y.Z)", m.Version)
	}
	if m.Engines.Orca == "" {
		t.Error("engines.orca must be non-empty")
	}
	if len(m.Contributes.VMRecipes) == 0 {
		t.Fatal("contributes.vmRecipes must have >= 1 entry")
	}
	for i, r := range m.Contributes.VMRecipes {
		if r.Path == "" {
			t.Errorf("vmRecipes[%d].path is empty", i)
			continue
		}
		if filepath.IsAbs(r.Path) {
			t.Errorf("vmRecipes[%d].path %q must be relative", i, r.Path)
		}
		full := filepath.Join(pd, r.Path)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("vmRecipes[%d].path %q does not exist on disk: %v", i, r.Path, err)
		}
	}
}

// --- recipe ---

type recipe struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Create        string `json:"create"`
	Suspend       string `json:"suspend"`
	Resume        string `json:"resume"`
	Destroy       string `json:"destroy"`
}

func TestRecipe(t *testing.T) {
	pd := pluginDir(t)
	data, err := os.ReadFile(filepath.Join(pd, "recipes", "nexus3.json"))
	if err != nil {
		t.Fatalf("read recipes/nexus3.json: %v", err)
	}
	var r recipe
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("parse recipes/nexus3.json: %v", err)
	}

	if r.SchemaVersion != 1 {
		t.Errorf("schemaVersion: want 1, got %d", r.SchemaVersion)
	}

	idRe := regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	if !idRe.MatchString(r.ID) {
		t.Errorf("id %q does not match ^[a-z0-9][a-z0-9._-]{0,63}$", r.ID)
	}
	if r.Name == "" {
		t.Error("name must be non-empty")
	}
	if r.Create == "" {
		t.Error("create must be non-empty")
	}

	// suspend + resume: both present or both absent
	hasSuspend := r.Suspend != ""
	hasResume := r.Resume != ""
	if hasSuspend != hasResume {
		t.Errorf("suspend and resume must both be present or both absent (suspend=%q resume=%q)", r.Suspend, r.Resume)
	}

	// destroy: if present, must be non-empty (already enforced by non-empty string; "none" is valid)
	// The zero value means absent — nothing to check here beyond what the struct already gives us.
	// But if the raw JSON has an explicit empty string, that would be caught by the unmarshal.
	// We check: if destroy key is explicitly set to empty string, fail.
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	if v, ok := raw["destroy"]; ok {
		if s, _ := v.(string); s == "" {
			t.Error("destroy key is present but empty (must be a non-empty command or \"none\")")
		}
	}
}
