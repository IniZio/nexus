package main

import (
	"os"
	"testing"

	"github.com/nexus/nexus/cmd/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractConfigToPlugin(t *testing.T) {
	// Create test project with some templates
	testDir := t.TempDir()

	// Create source directories and files
	rulesDir := testDir + "/.nexus/templates/rules"
	require.NoError(t, os.MkdirAll(rulesDir, 0755))
	require.NoError(t, os.WriteFile(rulesDir+"/team-standards.md", []byte("# Team Standards\n- Use Go"), 0644))

	skillsDir := testDir + "/.nexus/templates/skills"
	require.NoError(t, os.MkdirAll(skillsDir, 0755))
	require.NoError(t, os.WriteFile(skillsDir+"/code-review.yaml", []byte("name: code-review\ndescription: Review code"), 0644))

	// Change to test directory
	oldDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldDir)
	require.NoError(t, os.Chdir(testDir))

	// Test extraction
	err = internal.ExtractConfigToPlugin("team-config", true, true, false)
	require.NoError(t, err)

	// Verify plugin was created
	pluginDir := ".nexus/plugins/team-config"
	assert.DirExists(t, pluginDir)
	assert.FileExists(t, pluginDir+"/plugin.yaml")
	assert.FileExists(t, pluginDir+"/rules/team-standards.md")
	assert.FileExists(t, pluginDir+"/skills/code-review.yaml")

	// Verify manifest content
	manifest, err := os.ReadFile(pluginDir + "/plugin.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "name: team-config")
	assert.Contains(t, string(manifest), "rules:")
	assert.Contains(t, string(manifest), "skills:")
}
