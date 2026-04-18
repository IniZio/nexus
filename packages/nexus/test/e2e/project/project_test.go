//go:build e2e

package project_test

import (
	"testing"

	"github.com/inizio/nexus/packages/nexus/test/e2e/harness"
)

func TestProject(t *testing.T) {
	h := harness.New(t)

	// 1. Create a project
	var createRes struct {
		Project struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			RepoURL string `json:"repoUrl"`
		} `json:"project"`
	}
	h.MustCall("project.create", map[string]any{
		"name":    "e2e-test-project",
		"repoUrl": "git@github.com:test/e2e-test-project.git",
	}, &createRes)
	id := createRes.Project.ID
	if id == "" {
		t.Fatal("create: got empty project id")
	}
	if createRes.Project.Name != "e2e-test-project" {
		t.Fatalf("create: expected name %q, got %q", "e2e-test-project", createRes.Project.Name)
	}
	if createRes.Project.RepoURL != "git@github.com:test/e2e-test-project.git" {
		t.Fatalf("create: expected repoUrl %q, got %q", "git@github.com:test/e2e-test-project.git", createRes.Project.RepoURL)
	}

	// 2. List and verify the new project is present
	var listRes struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	h.MustCall("project.list", nil, &listRes)
	found := false
	for _, p := range listRes.Projects {
		if p.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("list: project %s not found after create", id)
	}

	// 3. Get by ID and verify fields
	var getRes struct {
		Project struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			RepoURL string `json:"repoUrl"`
		} `json:"project"`
	}
	h.MustCall("project.get", map[string]any{"id": id}, &getRes)
	if getRes.Project.ID != id {
		t.Fatalf("get: expected id %q, got %q", id, getRes.Project.ID)
	}
	if getRes.Project.Name != "e2e-test-project" {
		t.Fatalf("get: expected name %q, got %q", "e2e-test-project", getRes.Project.Name)
	}
	if getRes.Project.RepoURL != "git@github.com:test/e2e-test-project.git" {
		t.Fatalf("get: expected repoUrl %q, got %q", "git@github.com:test/e2e-test-project.git", getRes.Project.RepoURL)
	}

	// 4. Delete the project
	var removeRes struct {
		Removed bool `json:"removed"`
	}
	h.MustCall("project.remove", map[string]any{"id": id}, &removeRes)
	if !removeRes.Removed {
		t.Fatal("remove: expected removed=true")
	}

	// 5. List again and verify it's gone
	h.MustCall("project.list", nil, &listRes)
	for _, p := range listRes.Projects {
		if p.ID == id {
			t.Fatalf("list: project %s still present after remove", id)
		}
	}
}
