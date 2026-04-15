package handlers

import (
	"strconv"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

const (
	sandboxDefaultMemoryMiB = 1024
	sandboxDefaultVCPUs     = 1
	sandboxMaxMemoryMiB     = 4096
	sandboxMaxVCPUs         = 4
)

type sandboxResourcePolicy struct {
	defaultMemMiB int
	defaultVCPUs  int
	maxMemMiB     int
	maxVCPUs      int
}

func applySandboxResourcePolicy(options map[string]string, repo store.SandboxResourceSettingsRepository) map[string]string {
	if options == nil {
		options = map[string]string{}
	}

	policy := sandboxResourcePolicyFromRepository(repo)
	memMiB := positiveIntOption(options, "mem_mib", policy.defaultMemMiB)
	vcpus := positiveIntOption(options, "vcpus", policy.defaultVCPUs)
	if vcpus <= 0 {
		vcpus = positiveIntOption(options, "vcpu_count", policy.defaultVCPUs)
	}

	if policy.maxMemMiB > 0 && memMiB > policy.maxMemMiB {
		memMiB = policy.maxMemMiB
	}
	if policy.maxVCPUs > 0 && vcpus > policy.maxVCPUs {
		vcpus = policy.maxVCPUs
	}

	options["mem_mib"] = strconv.Itoa(memMiB)
	options["vcpus"] = strconv.Itoa(vcpus)
	return options
}

func sandboxResourcePolicyFromRepository(repo store.SandboxResourceSettingsRepository) sandboxResourcePolicy {
	policy := sandboxResourcePolicy{
		defaultMemMiB: sandboxDefaultMemoryMiB,
		defaultVCPUs:  sandboxDefaultVCPUs,
		maxMemMiB:     sandboxMaxMemoryMiB,
		maxVCPUs:      sandboxMaxVCPUs,
	}
	if repo == nil {
		return policy
	}
	row, ok, err := repo.GetSandboxResourceSettings()
	if err != nil || !ok {
		return policy
	}
	policy = sandboxResourcePolicy{
		defaultMemMiB: row.DefaultMemoryMiB,
		defaultVCPUs:  row.DefaultVCPUs,
		maxMemMiB:     row.MaxMemoryMiB,
		maxVCPUs:      row.MaxVCPUs,
	}
	if policy.defaultMemMiB <= 0 {
		policy.defaultMemMiB = sandboxDefaultMemoryMiB
	}
	if policy.defaultVCPUs <= 0 {
		policy.defaultVCPUs = sandboxDefaultVCPUs
	}
	if policy.maxMemMiB <= 0 {
		policy.maxMemMiB = sandboxMaxMemoryMiB
	}
	if policy.maxVCPUs <= 0 {
		policy.maxVCPUs = sandboxMaxVCPUs
	}

	if policy.maxMemMiB > 0 && policy.defaultMemMiB > policy.maxMemMiB {
		policy.defaultMemMiB = policy.maxMemMiB
	}
	if policy.maxVCPUs > 0 && policy.defaultVCPUs > policy.maxVCPUs {
		policy.defaultVCPUs = policy.maxVCPUs
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
