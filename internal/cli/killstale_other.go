//go:build !linux

package cli

import "github.com/IniZio/nexus/internal/core/domain"

// killStaleNetnsGroup is a no-op off Linux: only cloud-hypervisor records a
// netns child.
func killStaleNetnsGroup(domain.Sandbox) {}
