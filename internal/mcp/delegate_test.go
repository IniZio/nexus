package mcp

import (
	"strings"
	"testing"
)

func TestDelegateWorktreeCreate_RejectsAllowedBranches(t *testing.T) {
	args := delegateWorktreeCreateArgs{
		RepoPath:        "/tmp/testrepo",
		Handle:          "testproject/test-branch",
		AllowedBranches: []string{"refs/heads/**"},
	}
	err := validateDelegateWorktreeCreate(args)
	if err == nil {
		t.Fatal("expected error when allowed_branches is set, got nil")
	}
	if !strings.Contains(err.Error(), "allowed_branches") {
		t.Errorf("error %q does not mention allowed_branches", err.Error())
	}
}

func TestDelegateWorktreeCreate_ValidArgs_Pass(t *testing.T) {
	args := delegateWorktreeCreateArgs{
		RepoPath: "/tmp/testrepo",
		Handle:   "testproject/test-branch",
		ImageRef: "nexus3:latest",
	}
	if err := validateDelegateWorktreeCreate(args); err != nil {
		t.Errorf("valid args should not fail: %v", err)
	}
}

func TestDelegateWorktreeCreate_MissingRepoPath(t *testing.T) {
	args := delegateWorktreeCreateArgs{Handle: "proj/name"}
	if err := validateDelegateWorktreeCreate(args); err == nil {
		t.Fatal("expected error for missing repo_path")
	}
}

func TestDelegateWorktreeCreate_MissingHandle(t *testing.T) {
	args := delegateWorktreeCreateArgs{RepoPath: "/tmp/repo"}
	if err := validateDelegateWorktreeCreate(args); err == nil {
		t.Fatal("expected error for missing handle")
	}
}
