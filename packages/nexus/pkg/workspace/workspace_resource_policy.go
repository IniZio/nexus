package workspace

import (
	"strconv"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

const (
	SandboxDefaultMemoryMiB = 1024
	SandboxDefaultVCPUs     = 1
	SandboxMaxMemoryMiB     = 4096
	SandboxMaxVCPUs         = 4
)

type sandboxResourcePolicy struct {
	DefaultMemMiB int
	DefaultVCPUs  int
	MaxMemMiB     int
	MaxVCPUs      int
}

func applySandboxResourcePolicy(options map[string]string, repo store.SandboxResourceSettingsRepository) map[string]string {
	if options == nil {
		options = map[string]string{}
	}

	policy := SandboxResourcePolicyFromRepository(repo)
	memMiB := positiveIntOption(options, "mem_mib", policy.DefaultMemMiB)
	vcpus := positiveIntOption(options, "vcpus", policy.DefaultVCPUs)
	if vcpus <= 0 {
		vcpus = positiveIntOption(options, "vcpu_count", policy.DefaultVCPUs)
	}

	if policy.MaxMemMiB > 0 && memMiB > policy.MaxMemMiB {
		memMiB = policy.MaxMemMiB
	}
	if policy.MaxVCPUs > 0 && vcpus > policy.MaxVCPUs {
		vcpus = policy.MaxVCPUs
	}

	options["mem_mib"] = strconv.Itoa(memMiB)
	options["vcpus"] = strconv.Itoa(vcpus)
	return options
}

// SandboxResourcePolicyFromRepository is exported for use by the daemon package.
func SandboxResourcePolicyFromRepository(repo store.SandboxResourceSettingsRepository) sandboxResourcePolicy {
	policy := sandboxResourcePolicy{
		DefaultMemMiB: SandboxDefaultMemoryMiB,
		DefaultVCPUs:  SandboxDefaultVCPUs,
		MaxMemMiB:     SandboxMaxMemoryMiB,
		MaxVCPUs:      SandboxMaxVCPUs,
	}
	if repo == nil {
		return policy
	}
	row, ok, err := repo.GetSandboxResourceSettings()
	if err != nil || !ok {
		return policy
	}
	policy = sandboxResourcePolicy{
		DefaultMemMiB: row.DefaultMemoryMiB,
		DefaultVCPUs:  row.DefaultVCPUs,
		MaxMemMiB:     row.MaxMemoryMiB,
		MaxVCPUs:      row.MaxVCPUs,
	}
	if policy.DefaultMemMiB <= 0 {
		policy.DefaultMemMiB = SandboxDefaultMemoryMiB
	}
	if policy.DefaultVCPUs <= 0 {
		policy.DefaultVCPUs = SandboxDefaultVCPUs
	}
	if policy.MaxMemMiB <= 0 {
		policy.MaxMemMiB = SandboxMaxMemoryMiB
	}
	if policy.MaxVCPUs <= 0 {
		policy.MaxVCPUs = SandboxMaxVCPUs
	}

	if policy.MaxMemMiB > 0 && policy.DefaultMemMiB > policy.MaxMemMiB {
		policy.DefaultMemMiB = policy.MaxMemMiB
	}
	if policy.MaxVCPUs > 0 && policy.DefaultVCPUs > policy.MaxVCPUs {
		policy.DefaultVCPUs = policy.MaxVCPUs
	}
	return policy
}

func positiveIntOption(options map[string]string, key string, fallback int) int {
	if options == nil {
		return fallback
	}
	raw := strings.TrimSpace(options[key])
	if raw == "" {
		return fallback
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val <= 0 {
		return fallback
	}
	return val
}
