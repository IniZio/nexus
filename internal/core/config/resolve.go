package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Defaults holds the built-in fallback values used when neither an explicit
// flag nor a project config provides an override.
type Defaults struct {
	// Image is the default container image name.
	Image string
	// Memory is the default sandbox RAM in MiB.
	Memory int
	// VCPUs is the default number of virtual CPUs.
	VCPUs int
}

// Flags mirrors the subset of CLI flags that the precedence resolver cares
// about. Use the zero value for "flag not supplied by the user".
//
// Pointers are used so the resolver can distinguish "flag was set to zero"
// from "flag was not set". For string fields the empty string means "not set"
// (CLI parsers never produce a meaningful empty image name).
type Flags struct {
	// Image is the --image flag value. Empty means the flag was not given.
	Image string
	// Memory is the --memory flag value. nil means the flag was not given.
	Memory *int
	// VCPUs is the --vcpus flag value. nil means the flag was not given.
	VCPUs *int
	// EgressAllow is the set of extra hosts passed via --egress-allow flags.
	// nil means the flag was not given; a non-nil empty slice means the flag
	// was given with no values.
	EgressAllow []string
	// Mounts is the set of mounts passed via --mount flags.
	Mounts []string
}

// Resolved is the final, merged sandbox configuration after applying the
// precedence chain: explicit CLI flag > project config > built-in default.
type Resolved struct {
	Image       string
	MemoryMiB   int
	VCPUs       int
	EgressAllow []string
	Mounts      []string
}

// Resolve merges f (CLI flags), cfg (project config), and d (built-in
// defaults) according to the precedence chain:
//
//	explicit CLI flag > project config > built-in default
//
// Resolve is a pure function: it does not read files, environment variables,
// or any external state.
func Resolve(f Flags, cfg Config, d Defaults) Resolved {
	var r Resolved

	// Image: flag > config > default.
	switch {
	case f.Image != "":
		r.Image = f.Image
	case cfg.Sandbox.Image != "":
		r.Image = cfg.Sandbox.Image
	default:
		r.Image = d.Image
	}

	// Memory: flag > config (non-zero) > default.
	switch {
	case f.Memory != nil:
		r.MemoryMiB = *f.Memory
	case cfg.Sandbox.Memory != 0:
		r.MemoryMiB = cfg.Sandbox.Memory
	default:
		r.MemoryMiB = d.Memory
	}

	// VCPUs: flag > config (non-zero) > default.
	switch {
	case f.VCPUs != nil:
		r.VCPUs = *f.VCPUs
	case cfg.Sandbox.VCPUs != 0:
		r.VCPUs = cfg.Sandbox.VCPUs
	default:
		r.VCPUs = d.VCPUs
	}

	// EgressAllow is the one field that does NOT follow flag > config. It is
	// ADDITIVE: the result is the union of --allow-host flags and the project
	// config's hosts, flags first.
	//
	// Egress hosts are a set of needs, not a single choice. The project declares
	// what the repo always requires (a module proxy, a package registry); a
	// particular run may additionally need one more host. Under flag > config,
	// typing a single --allow-host would silently DROP every project host and
	// break the build, with no error and no clue as to why. Additive is also
	// what --allow-host has always meant: the flag is repeatable and each use
	// adds a host.
	//
	// No built-in default: the built-in allowlist lives in the perimeter layer
	// and the agent profile, and is applied on top of this by resolveAgentPosture.
	r.EgressAllow = append(append([]string{}, f.EgressAllow...), cfg.Egress.Allow...)

	// Mounts: flag > config. No built-in default.
	switch {
	case f.Mounts != nil:
		r.Mounts = f.Mounts
	default:
		r.Mounts = cfg.Sandbox.Mounts
	}

	return r
}

// ResolveMounts takes a slice of mount entries in "hostPath:guestPath" format
// and makes any relative hostPath absolute relative to configDir — the
// directory that contains the nexus3.yaml file.
//
// This must use configDir, not the process working directory: a user running
// nexus3 from a sub-directory of their repo expects ".:/work" to refer to the
// repo root where nexus3.yaml lives, not to whatever directory they happen to
// be in at the time.
//
// A mount entry without a colon is returned as-is (invalid; let the caller
// validate it). An empty configDir leaves relative paths unresolved (used when
// no config file was found).
func ResolveMounts(mounts []string, configDir string) ([]string, error) {
	if len(mounts) == 0 {
		return mounts, nil
	}
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		idx := strings.Index(m, ":")
		if idx < 0 {
			// No separator — pass through unchanged; caller validates.
			out = append(out, m)
			continue
		}
		host := m[:idx]
		guest := m[idx+1:]

		if !filepath.IsAbs(host) && configDir != "" {
			abs, err := filepath.Abs(filepath.Join(configDir, host))
			if err != nil {
				return nil, fmt.Errorf("config: resolve mount %q: %w", m, err)
			}
			host = abs
		}
		out = append(out, host+":"+guest)
	}
	return out, nil
}
