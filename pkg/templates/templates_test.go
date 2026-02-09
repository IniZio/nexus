package templates

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
)

func TestMerge(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "templates-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	baseDir := tempDir
	os.MkdirAll(filepath.Join(baseDir, "templates/skills"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "templates/rules"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "remotes/repo1/templates/skills"), 0755)

	os.WriteFile(filepath.Join(baseDir, "templates/skills/skill1.yaml"), []byte("name: skill1\nversion: \"1.0\""), 0644)
	os.WriteFile(filepath.Join(baseDir, "templates/rules/rule1.md"), []byte("---\ntitle: rule1\n---\nRule 1 Content"), 0644)
	os.WriteFile(filepath.Join(baseDir, "remotes/repo1/templates/skills/skill1.yaml"), []byte("version: \"2.0\"\nnew_field: value"), 0644)

	m := NewManager(baseDir)
	data, err := m.Merge(baseDir, nil, nil)

	assert.NoError(t, err)
	assert.NotNil(t, data)

	basePlugin := data.Plugins["base"]
	assert.NotNil(t, basePlugin)

	skill1 := basePlugin.Skills["skill1"].(map[string]interface{})
	assert.Equal(t, "skill1", skill1["name"])
	assert.Equal(t, "1.0", skill1["version"])
	// new_field from remote should not be present since base takes precedence
	assert.NotContains(t, skill1, "new_field")

	rule1 := basePlugin.Rules["rule1"].(map[string]interface{})
	assert.Equal(t, "rule1", rule1["title"])
	assert.Equal(t, "Rule 1 Content", rule1["content"])
}

func TestRenderTemplate(t *testing.T) {
	m := NewManager("")
	tmpl := "Hello {{.Name}}!"
	data := map[string]string{"Name": "World"}

	result, err := m.RenderTemplate(tmpl, data)
	assert.NoError(t, err)
	assert.Equal(t, "Hello World!", result)
}

func TestGetRepoDir(t *testing.T) {
	m := NewManager("/base")
	repoDir := m.GetRepoDir("my-repo")
	assert.Equal(t, "/base/remotes/my-repo", repoDir)
}

func TestPullWithoutUpdate(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "templates-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	m := NewManager(tempDir)

	// Test that GetRepoDir works correctly
	repoDir := m.GetRepoDir("my-repo")
	assert.Equal(t, filepath.Join(tempDir, "remotes", "my-repo"), repoDir)

	// Test PullWithoutUpdate with non-existent repo (will fail on clone, which is expected)
	// This verifies the method structure is correct
	repo := TemplateRepo{
		URL:    "https://github.com/owner/repo",
		Branch: "main",
	}

	// Verify the repo directory path is computed correctly before any clone attempt
	expectedRepoDir := filepath.Join(tempDir, "remotes", "repo")
	assert.Equal(t, expectedRepoDir, m.GetRepoDir("repo"))

	// Test that existing directories are detected (simulate cached repo)
	os.MkdirAll(expectedRepoDir, 0755)
	err = m.PullWithoutUpdate(repo)
	assert.NoError(t, err) // Should succeed because repo already exists
}

func TestGetRepoSHA(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "templates-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	m := NewManager(tempDir)

	// Test with non-existent repo
	repo := TemplateRepo{URL: "https://github.com/owner/nonexistent"}
	_, err = m.GetRepoSHA(repo)
	assert.Error(t, err)

	// Create a fake git repo for testing
	repoDir := m.GetRepoDir("test-repo")
	os.MkdirAll(repoDir, 0755)

	// Initialize a bare git repo to get a valid SHA
	r, err := git.PlainInit(repoDir, false)
	assert.NoError(t, err)

	// Create a commit
	wt, err := r.Worktree()
	assert.NoError(t, err)

	os.WriteFile(filepath.Join(repoDir, "test.txt"), []byte("test"), 0644)
	_, err = wt.Add("test.txt")
	assert.NoError(t, err)

	_, err = wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	assert.NoError(t, err)

	// Now test GetRepoSHA
	sha, err := m.GetRepoSHA(TemplateRepo{URL: "https://github.com/owner/test-repo"})
	assert.NoError(t, err)
	assert.Len(t, sha, 40) // SHA length
}

func TestMergeConfigTemplates_Services(t *testing.T) {
	base := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				Command:   "npm start",
				Port:      3000,
				DependsOn: []string{"db"},
				Env:       map[string]string{"DB_HOST": "localhost"},
			},
			"db": {
				Command: "postgres",
				Port:    5432,
			},
		},
	}

	overlay := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				Port: 8080, // Override port
				Env:  map[string]string{"NODE_ENV": "production"},
			},
			"cache": {
				Command: "redis-server",
				Port:    6379,
			},
		},
	}

	result, err := MergeConfigTemplates(base, overlay)
	assert.NoError(t, err)

	// Check that web has overlay port
	assert.Equal(t, 8080, result.Services["web"].Port)
	// Check that web kept base command
	assert.Equal(t, "npm start", result.Services["web"].Command)
	// Check that web env is merged
	assert.Equal(t, "localhost", result.Services["web"].Env["DB_HOST"])
	assert.Equal(t, "production", result.Services["web"].Env["NODE_ENV"])
	// Check that web kept base dependsOn
	assert.Contains(t, result.Services["web"].DependsOn, "db")
	// Check that new service from overlay is present
	assert.Equal(t, "redis-server", result.Services["cache"].Command)
	// Check that base service not in overlay is present
	assert.Equal(t, "postgres", result.Services["db"].Command)
}

func TestMergeConfigTemplates_Environment(t *testing.T) {
	base := ConfigTemplate{
		Environment: map[string]string{
			"FOO": "base_foo",
			"BAR": "base_bar",
		},
	}

	overlay := ConfigTemplate{
		Environment: map[string]string{
			"BAR": "overlay_bar",
			"BAZ": "overlay_baz",
		},
	}

	result, err := MergeConfigTemplates(base, overlay)
	assert.NoError(t, err)

	// Check that overlay values override base
	assert.Equal(t, "overlay_bar", result.Environment["BAR"])
	// Check that base values are preserved when not overridden
	assert.Equal(t, "base_foo", result.Environment["FOO"])
	// Check that new overlay values are added
	assert.Equal(t, "overlay_baz", result.Environment["BAZ"])
}

func TestMergeConfigTemplates_VolumesAndNetworks(t *testing.T) {
	base := ConfigTemplate{
		Volumes: map[string]VolumeTemplate{
			"data_volume": {Driver: "local"},
		},
		Networks: map[string]NetworkTemplate{
			"frontend": {Driver: "bridge"},
		},
	}

	overlay := ConfigTemplate{
		Volumes: map[string]VolumeTemplate{
			"data_volume": {Driver: "nfs"}, // Override
			"cache_volume": {Driver: "local"},
		},
		Networks: map[string]NetworkTemplate{
			"frontend": {Driver: "overlay"}, // Override
			"backend": {Driver: "bridge"},
		},
	}

	result, err := MergeConfigTemplates(base, overlay)
	assert.NoError(t, err)

	// Check that overlay volume overrides base
	assert.Equal(t, "nfs", result.Volumes["data_volume"].Driver)
	// Check that new overlay volume is added
	assert.Equal(t, "local", result.Volumes["cache_volume"].Driver)
	// Check that overlay network overrides base
	assert.Equal(t, "overlay", result.Networks["frontend"].Driver)
	// Check that new overlay network is added
	assert.Equal(t, "bridge", result.Networks["backend"].Driver)
}

func TestMergeConfigTemplates_DependsOnOrdering(t *testing.T) {
	base := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				Command:   "npm start",
				DependsOn: []string{"db", "cache"},
			},
			"db":    {Command: "postgres"},
			"cache": {Command: "memcached"},
		},
	}

	overlay := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				DependsOn: []string{"redis"},
			},
			"redis": {Command: "redis-server"},
		},
	}

	result, err := MergeConfigTemplates(base, overlay)
	assert.NoError(t, err)

	// Check that overlay dependsOn comes first, then base (without duplicates)
	deps := result.Services["web"].DependsOn
	assert.Equal(t, []string{"redis", "db", "cache"}, deps)
}

func TestMergeConfigTemplates_CircularDependency(t *testing.T) {
	base := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				Command:   "npm start",
				DependsOn: []string{"db"},
			},
			"db": {
				Command:   "postgres",
				DependsOn: []string{"web"}, // Circular!
			},
		},
	}

	_, err := MergeConfigTemplates(base, ConfigTemplate{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestMergeConfigTemplates_InvalidDependsOn(t *testing.T) {
	base := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				Command:   "npm start",
				DependsOn: []string{"nonexistent"},
			},
		},
	}

	_, err := MergeConfigTemplates(base, ConfigTemplate{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "depends on non-existent service")
}

func TestMergeConfigTemplates_ServiceVolumesAndNetworks(t *testing.T) {
	base := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				Command:   "npm start",
				Volumes:   []string{"vol1:/data", "vol2:/cache"},
				Networks:  []string{"net1"},
			},
		},
	}

	overlay := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {
				Volumes:  []string{"vol3:/logs"},
				Networks: []string{"net2"},
			},
		},
	}

	result, err := MergeConfigTemplates(base, overlay)
	assert.NoError(t, err)

	// Check that volumes are merged
	volumes := result.Services["web"].Volumes
	assert.Contains(t, volumes, "vol1:/data")
	assert.Contains(t, volumes, "vol2:/cache")
	assert.Contains(t, volumes, "vol3:/logs")

	// Check that networks are merged
	networks := result.Services["web"].Networks
	assert.Contains(t, networks, "net1")
	assert.Contains(t, networks, "net2")
}

func TestMergeConfigTemplates_EmptyOverlay(t *testing.T) {
	base := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {Command: "npm start"},
		},
		Environment: map[string]string{"FOO": "bar"},
	}

	result, err := MergeConfigTemplates(base, ConfigTemplate{})
	assert.NoError(t, err)

	assert.Equal(t, "npm start", result.Services["web"].Command)
	assert.Equal(t, "bar", result.Environment["FOO"])
}

func TestMergeConfigTemplates_EmptyBase(t *testing.T) {
	overlay := ConfigTemplate{
		Services: map[string]ServiceTemplate{
			"web": {Command: "npm start"},
		},
		Environment: map[string]string{"FOO": "bar"},
	}

	result, err := MergeConfigTemplates(ConfigTemplate{}, overlay)
	assert.NoError(t, err)

	assert.Equal(t, "npm start", result.Services["web"].Command)
	assert.Equal(t, "bar", result.Environment["FOO"])
}
