// Package config loads and represents per-repository nexus3 configuration
// from a nexus3.yaml file found by walking up from a start directory.
//
// Discovery: Load walks from startDir toward the filesystem root, stopping
// at the first directory that contains a .git entry (the repository root).
// An absent nexus3.yaml is not an error; a present but malformed file is.
// An unknown YAML key is a hard error (security-relevant: a typo in an
// egress allowlist silently disables the intended host).
//
// Usage pattern (caller drives precedence):
//
//	cfg, cfgPath, err := config.Load(startDir)
//	opts := config.Resolve(explicitFlags, cfg, config.Defaults())
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SupportedVersion is the highest nexus3.yaml schema version this binary understands.
const SupportedVersion = 1

// MinSupportedVersion is the lowest nexus3.yaml schema version this binary accepts.
// Files declaring a version below this must be re-created before use.
const MinSupportedVersion = 1

// EgressConfig holds per-repo egress allow rules.
type EgressConfig struct {
	// Allow lists additional hostnames the sandbox may reach.
	// These are merged on top of the built-in perimeter allowlist by the
	// precedence resolver; they do not replace it.
	Allow []string `yaml:"allow"`
}

// Mounts is a list of host→guest mount entries in nexus3.yaml.
// Each element may be either:
//   - a short string "host:guest" or "host:guest:ro"
//   - a YAML mapping {source: host, target: guest, read_only: true|false}
//
// Both forms normalise to a "host:guest" or "host:guest:ro" canonical string.
// Consumers treat the slice as []string — use []string(m) to convert.
type Mounts []string

// UnmarshalYAML implements yaml.Unmarshaler so that each element of a YAML
// sequence may be either a scalar string or a {source,target,read_only} mapping.
func (m *Mounts) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: mounts must be a YAML sequence, got kind %d", value.Kind)
	}
	out := make(Mounts, 0, len(value.Content))
	for _, item := range value.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			// Short form: "host:guest" or "host:guest:ro"
			out = append(out, item.Value)
		case yaml.MappingNode:
			// Long form: {source: ..., target: ..., read_only: true|false}
			var entry struct {
				Source   string `yaml:"source"`
				Target   string `yaml:"target"`
				ReadOnly bool   `yaml:"read_only"`
			}
			if err := item.Decode(&entry); err != nil {
				return fmt.Errorf("config: mounts entry: %w", err)
			}
			if entry.Source == "" {
				return fmt.Errorf("config: mounts entry: missing required field 'source'")
			}
			if entry.Target == "" {
				return fmt.Errorf("config: mounts entry: missing required field 'target'")
			}
			s := entry.Source + ":" + entry.Target
			if entry.ReadOnly {
				s += ":ro"
			}
			out = append(out, s)
		default:
			return fmt.Errorf("config: mounts entry must be a string or mapping, got YAML kind %d", item.Kind)
		}
	}
	*m = out
	return nil
}

// SandboxConfig holds per-repo sandbox defaults.
type SandboxConfig struct {
	// Image is the container image name used when no --image flag is given.
	Image string `yaml:"image"`

	// Memory is the sandbox RAM in MiB. Zero means "use the built-in default".
	Memory int `yaml:"memory"`

	// VCPUs is the number of virtual CPUs. Zero means "use the built-in default".
	VCPUs int `yaml:"vcpus"`

	// Mounts is a list of host→guest mount entries accepted in short string
	// form ("host:guest[:ro]") or long object form ({source, target, read_only}).
	// Both normalise to the canonical "host:guest[:ro]" string.
	// Host-relative paths are resolved against the nexus3.yaml directory.
	Mounts Mounts `yaml:"mounts"`

	// Agent is the default agent profile name applied when no --agent flag is
	// given at sandbox create time. Must be a registered cred.AgentProfile name
	// (e.g. "claude-code"). Empty (the default) means no agent by default.
	//
	// User-global location: $XDG_CONFIG_HOME/nexus3/config.yaml
	// (falls back to ~/.config/nexus3/config.yaml).
	//
	// Example:
	//   version: 1
	//   sandbox:
	//     agent: claude-code
	Agent string `yaml:"agent"`

	// MemoryMax is the RAM hotplug ceiling in MiB. Zero means "use the
	// built-in default" (typically 4× boot memory or 4096 MiB, whichever is
	// larger). Must exceed the boot Memory when both are set.
	//
	// Applies to both the project nexus3.yaml and the user-global config.yaml.
	// CLI --memory-max always wins over either config source.
	MemoryMax int `yaml:"memory_max"`
}

// ImageGCConfig holds image garbage collection settings.
type ImageGCConfig struct {
	// FreeSpaceFloorGiB is the minimum free disk space (in GiB) required on
	// the filesystem backing ~/.local/state/nexus3 before a build starts.
	// When free space falls below this floor, automatic GC runs first.
	// Zero means "use the built-in default" (DefaultGCFreeSpaceFloorGiB = 15 GiB).
	FreeSpaceFloorGiB int `yaml:"free_space_floor_gib"`

	// KeepNewestBuilderImages, when positive, retains up to this many of the
	// most-recently created builder images regardless of sandbox references.
	// Zero (the default) disables the retention budget: only sandbox-referenced
	// and base images are kept.
	KeepNewestBuilderImages int `yaml:"keep_newest_builder_images"`
}

// BuilderConfig holds per-build builder VM settings.
type BuilderConfig struct {
	// MemoryMiB is the builder VM guest memory in mebibytes. Zero means
	// "use the built-in default" (builder.DefaultBuilderMemMiB = 8192 MiB).
	// Must be at least 1024 MiB when set. CLI --builder-memory always takes
	// precedence over this setting.
	MemoryMiB int `yaml:"memory_mib"`
}

// Config is the in-memory representation of a nexus3.yaml file.
// A zero Config is valid and means "no project-level overrides".
type Config struct {
	Egress  EgressConfig  `yaml:"egress"`
	Sandbox SandboxConfig `yaml:"sandbox"`
	Image   ImageGCConfig `yaml:"image"`
	Builder BuilderConfig `yaml:"builder"`
}

// fileConfig is the on-disk YAML shape, including the required version field.
// Used only by parse; callers work with Config.
type fileConfig struct {
	// Version is a *int so that nil (field absent from file) is distinguishable
	// from 0 (field present but set to zero). A missing version is a hard error.
	Version *int          `yaml:"version"`
	Egress  EgressConfig  `yaml:"egress"`
	Sandbox SandboxConfig `yaml:"sandbox"`
	Image   ImageGCConfig `yaml:"image"`
	Builder BuilderConfig `yaml:"builder"`
}

// configFileName is the well-known name discovered during Load.
const configFileName = "nexus3.yaml"

// Load walks up from startDir looking for nexus3.yaml, stopping at the repo
// root (a directory containing .git) or the filesystem root.
//
// Returns:
//   - cfg: the parsed Config (zero value when no file is found)
//   - filePath: absolute path of the loaded file, or "" when no file was found
//   - err: non-nil only when the file exists but cannot be parsed, when an
//     unknown YAML key is present, or when the version field is absent or
//     outside [MinSupportedVersion, SupportedVersion]
//
// An absent nexus3.yaml is NOT an error.
func Load(startDir string) (Config, string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return Config{}, "", fmt.Errorf("config.Load: resolve startDir %q: %w", startDir, err)
	}

	dir := abs
	for {
		candidate := filepath.Join(dir, configFileName)
		data, err := os.ReadFile(candidate)
		if err == nil {
			// File found: parse and return.
			cfg, parseErr := parse(data)
			if parseErr != nil {
				return Config{}, "", fmt.Errorf("config: parse %q: %w", candidate, parseErr)
			}
			return cfg, candidate, nil
		}
		if !os.IsNotExist(err) {
			// Unexpected I/O error (permission denied, etc.) — propagate.
			return Config{}, "", fmt.Errorf("config: read %q: %w", candidate, err)
		}

		// Stop at a .git boundary (repository root).
		if hasGitRoot(dir) {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root.
			break
		}
		dir = parent
	}

	// No file found — not an error.
	return Config{}, "", nil
}

// parse decodes YAML data into a Config, rejecting any unknown keys.
// Unknown keys are a hard error: a typo in the egress allowlist silently
// disables the intended host, which is a security-relevant failure.
//
// A missing or out-of-range version field is also a hard error. A missing
// version cannot be silently defaulted: that is how a future syntax version
// would silently misparse an old file, with no warning and no path to upgrade.
func parse(data []byte) (Config, error) {
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return Config{}, err
	}
	if fc.Version == nil {
		return Config{}, fmt.Errorf(
			"nexus3.yaml: missing required field \"version\" — add `version: %d` as the first line",
			SupportedVersion,
		)
	}
	v := *fc.Version
	if v < MinSupportedVersion || v > SupportedVersion {
		return Config{}, fmt.Errorf(
			"nexus3.yaml declares version %d; this nexus3 supports versions %d–%d — "+
				"upgrade nexus3 if the file is newer, or re-create the file if it is older",
			v, MinSupportedVersion, SupportedVersion,
		)
	}
	return Config{
		Egress:  fc.Egress,
		Sandbox: fc.Sandbox,
		Image:   fc.Image,
		Builder: fc.Builder,
	}, nil
}

// hasGitRoot returns true when dir contains a .git entry (file or directory),
// indicating it is the root of a git repository.
func hasGitRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
