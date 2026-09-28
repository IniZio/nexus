package service

import (
	"testing"
)

func TestZ6_ResolveAllowedBranchesNoWorkspaceNoGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	opts := CreateAndBootOptions{}
	got := resolveAllowedBranches(opts)
	if got != nil {
		t.Errorf("resolveAllowedBranches with no workspace returned %v; want nil", got)
	}
}
