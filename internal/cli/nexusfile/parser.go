// Package nexusfile parses Nexusfile project-task manifests.
//
// A Nexusfile is a TOML file with one or more named profile sections.
// Each section may contain bake, up, and down ordered command arrays.
// Top-level scalar keys (e.g. "$schema") are silently ignored.
//
//	"$schema" = "./schemas/nexusfile.schema.json"
//	[dev]
//	bake = ["apt-get update", "apt-get install -y docker-compose-plugin"]
//	up   = ["docker compose build --parallel", "docker compose up -d"]
//	down = ["docker compose down"]
package nexusfile

import (
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Section holds the ordered command arrays for one named profile.
type Section struct {
	Bake []string `toml:"bake"`
	Up   []string `toml:"up"`
	Down []string `toml:"down"`
}

// Nexusfile is the parsed representation of a Nexusfile.
type Nexusfile struct {
	sections map[string]Section
}

// ErrSectionNotFound is returned by Section when the named profile is absent.
var ErrSectionNotFound = errors.New("nexusfile: section not found")

// Load reads and parses the TOML Nexusfile at path.
// It returns an error if the file cannot be read or contains invalid TOML.
func Load(path string) (*Nexusfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("nexusfile: read %s: %w", path, err)
	}

	// Decode into a generic map so we can skip top-level scalar keys
	// (e.g. "$schema") without schema knowledge.
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("nexusfile: parse %s: %w", path, err)
	}

	sections := make(map[string]Section, len(raw))
	for k, v := range raw {
		// Only TOML tables (sections) are decoded as Section; scalar values
		// such as "$schema" are skipped.
		sub, ok := v.(map[string]any)
		if !ok {
			continue
		}
		sec := Section{
			Bake: toStringSlice(sub["bake"]),
			Up:   toStringSlice(sub["up"]),
			Down: toStringSlice(sub["down"]),
		}
		sections[k] = sec
	}

	return &Nexusfile{sections: sections}, nil
}

// Section returns the named profile section, or ErrSectionNotFound if absent.
func (n *Nexusfile) Section(name string) (Section, error) {
	sec, ok := n.sections[name]
	if !ok {
		return Section{}, fmt.Errorf("%w: %q", ErrSectionNotFound, name)
	}
	return sec, nil
}

// toStringSlice converts an any value (expected: []any from TOML array decode)
// to []string. Non-string elements and nil inputs are silently skipped.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
