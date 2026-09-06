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

// EgressSecret is one entry in the egress.secrets list.
// It binds a host-side env var to a set of target hosts.
// Path policies (GitHub or generic globs) live in EgressPolicy, not here.
//
// Short form (scalar in the YAML sequence):
//
//	"ENV@host1,host2"
//
// Long form (mapping in the YAML sequence):
//
//	env: ENV
//	hosts: [host1, host2]
type EgressSecret struct {
	// Env is the name of the host env var whose value is injected as a
	// Bearer / Authorization token for the listed hosts.
	Env string `yaml:"env"`

	// Hosts is the list of hostnames the secret is forwarded to.
	Hosts []string `yaml:"hosts"`
}

// EgressSecrets is a list of EgressSecret entries. Each element may be either
// a short scalar "ENV@host1,host2" or a long mapping form.
type EgressSecrets []EgressSecret

// UnmarshalYAML implements yaml.Unmarshaler so that each element of a YAML
// sequence may be either a scalar string "ENV@host1,host2" or a long mapping.
func (s *EgressSecrets) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: egress.secrets must be a YAML sequence, got kind %d", value.Kind)
	}
	out := make(EgressSecrets, 0, len(value.Content))
	for _, item := range value.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			// Short form: "ENV@host1,host2"
			entry, err := parseEgressSecretShort(item.Value)
			if err != nil {
				return fmt.Errorf("config: egress.secrets entry: %w", err)
			}
			out = append(out, entry)
		case yaml.MappingNode:
			// Long form: {env, hosts, repo?, paths?}
			// Manually check for unknown keys before decoding.
			knownKeys := map[string]bool{"env": true, "hosts": true}
			for i := 0; i+1 < len(item.Content); i += 2 {
				k := item.Content[i].Value
				if !knownKeys[k] {
					return fmt.Errorf("config: egress.secrets entry: unknown key %q", k)
				}
			}
			var entry EgressSecret
			if err := item.Decode(&entry); err != nil {
				return fmt.Errorf("config: egress.secrets entry: %w", err)
			}
			if entry.Env == "" {
				return fmt.Errorf("config: egress.secrets entry: missing required field 'env'")
			}
			if len(entry.Hosts) == 0 {
				return fmt.Errorf("config: egress.secrets entry: missing required field 'hosts'")
			}
			out = append(out, entry)
		default:
			return fmt.Errorf("config: egress.secrets entry must be a string or mapping, got YAML kind %d", item.Kind)
		}
	}
	*s = out
	return nil
}

// parseEgressSecretShort parses a short-form "ENV@host1,host2[,hostN]" secret spec.
func parseEgressSecretShort(spec string) (EgressSecret, error) {
	if spec == "" {
		return EgressSecret{}, fmt.Errorf("empty spec")
	}
	at := -1
	for i, c := range spec {
		if c == '@' {
			at = i
			break
		}
	}
	if at < 0 {
		return EgressSecret{}, fmt.Errorf("%q: want ENV@host[,host…]", spec)
	}
	env := spec[:at]
	if env == "" {
		return EgressSecret{}, fmt.Errorf("%q: env name must not be empty", spec)
	}
	hostPart := spec[at+1:]
	if hostPart == "" {
		return EgressSecret{}, fmt.Errorf("%q: hosts must not be empty", spec)
	}
	// Split comma-separated hosts.
	rawHosts := splitComma(hostPart)
	hosts := make([]string, 0, len(rawHosts))
	for _, h := range rawHosts {
		if h == "" {
			return EgressSecret{}, fmt.Errorf("%q: empty host in list", spec)
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		return EgressSecret{}, fmt.Errorf("%q: hosts must not be empty", spec)
	}
	return EgressSecret{Env: env, Hosts: hosts}, nil
}

// splitComma splits s on commas; not strings.Split to avoid importing strings.
func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// EgressPolicy is one destination-scoped path-policy entry.
// It restricts what URL paths can be reached on a specific host.
// Paths are method-aware anchored globs (optional "METHOD " prefix,
// e.g. "GET /repos/**" or "/repos/**" for any method).
type EgressPolicy struct {
	// Host is the target hostname (required).
	Host string `yaml:"host"`

	// Paths is the positive path allowlist (anchored globs). Default-deny:
	// only listed paths are allowed; all others are rejected. Required.
	Paths []string `yaml:"paths"`
}

// EgressPolicies is a list of EgressPolicy entries.
type EgressPolicies []EgressPolicy

// UnmarshalYAML implements yaml.Unmarshaler, enforcing unknown-key rejection
// and field-level validation for each egress.policy entry.
func (p *EgressPolicies) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: egress.policy must be a YAML sequence, got kind %d", value.Kind)
	}
	out := make(EgressPolicies, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("config: egress.policy entry must be a mapping, got YAML kind %d", item.Kind)
		}
		// Manually check for unknown keys (KnownFields(true) does not propagate
		// into custom unmarshalers; we enforce it here explicitly).
		knownKeys := map[string]bool{"host": true, "paths": true}
		for i := 0; i+1 < len(item.Content); i += 2 {
			k := item.Content[i].Value
			if !knownKeys[k] {
				return fmt.Errorf("config: egress.policy entry: unknown key %q", k)
			}
		}
		var entry EgressPolicy
		if err := item.Decode(&entry); err != nil {
			return fmt.Errorf("config: egress.policy entry: %w", err)
		}
		if entry.Host == "" {
			return fmt.Errorf("config: egress.policy entry: missing required field 'host'")
		}
		if len(entry.Paths) == 0 {
			return fmt.Errorf("config: egress.policy entry %q: missing required field 'paths' (must have at least one path)", entry.Host)
		}
		out = append(out, entry)
	}
	*p = out
	return nil
}

// EgressConfig holds per-repo egress allow rules.
type EgressConfig struct {
	// Allow lists additional hostnames the sandbox may reach.
	// These are merged on top of the built-in perimeter allowlist by the
	// precedence resolver; they do not replace it.
	Allow []string `yaml:"allow"`

	// Policy lists destination-scoped path-policy entries. Each entry restricts
	// what URL paths are reachable on a specific host. Path policies for GitHub
	// hosts are mandatory (D-PDE-16); the wiring layer enforces this.
	Policy EgressPolicies `yaml:"policy"`

	// Secrets lists host+credential bindings. Each entry attaches one env var
	// (looked up from the host environment) to one or more target hostnames.
	// Unlike Allow (reachability only), Secrets entries attach a credential and
	// are therefore security-critical — read from the trusted base ref only.
	Secrets EgressSecrets `yaml:"secrets"`
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

	// Nested enables nested virtualisation (D-N3N-02: must be opt-in, default
	// false). When true, /dev/kvm is exposed to the guest VM so inner VMs can
	// boot. This widens the isolation perimeter and MUST NOT be set from a
	// worktree branch — only the trusted ref (refs/remotes/origin/HEAD via
	// readTrustedRefBytes) is honoured, so no branch can grant itself /dev/kvm.
	Nested bool `yaml:"nested"`
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

// Parse decodes YAML data into a Config, rejecting any unknown keys.
// It is a public wrapper around the unexported parse function, intended for
// callers that obtain config bytes out-of-band (e.g. via `git show <ref>:nexus3.yaml`)
// rather than through the filesystem walk performed by Load.
//
// Parse and Load apply identical validation; the only difference is the source
// of the YAML bytes.
func Parse(data []byte) (Config, error) {
	return parse(data)
}

// hasGitRoot returns true when dir contains a .git entry (file or directory),
// indicating it is the root of a git repository.
func hasGitRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
