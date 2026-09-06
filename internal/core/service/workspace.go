package service

import (
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// WorkspaceMountHostPath returns the host path of the first LiveMount whose
// guest path is exactly "/workspace" or begins with "/workspace/". This is
// the authoritative implementation of the workspace-LiveMount check used by
// both hostWorkspacePath (service) and bootScratchDiskPresent (CLI).
//
// One copy of the string match: any change to the predicate here automatically
// applies to both callers.
func WorkspaceMountHostPath(liveMounts []domain.LiveMount) (hostPath string, ok bool) {
	for _, m := range liveMounts {
		if m.GuestPath == "/workspace" || strings.HasPrefix(m.GuestPath, "/workspace/") {
			return m.HostPath, true
		}
	}
	return "", false
}

// HasWorkspaceMount reports whether liveMounts contains a /workspace mount.
// It is the boolean form of WorkspaceMountHostPath, used by the CLI factory
// when only the presence flag (not the host path) is needed.
func HasWorkspaceMount(liveMounts []domain.LiveMount) bool {
	_, ok := WorkspaceMountHostPath(liveMounts)
	return ok
}
