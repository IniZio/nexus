package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionCmdRun(t *testing.T) {
	err := versionCmd.RunE(versionCmd, []string{})
	assert.NoError(t, err)
}

func TestRootCmdConfiguration(t *testing.T) {
	assert.NotNil(t, rootCmd)
	assert.Equal(t, "nexus", rootCmd.Use)
	assert.NotEmpty(t, rootCmd.Short)
	assert.NotEmpty(t, rootCmd.Long)
}

func TestVersionVariables(t *testing.T) {
	assert.NotEmpty(t, version)
	assert.NotEmpty(t, goVersion)
}

func TestBranchCmdHasChildren(t *testing.T) {
	assert.NotNil(t, branchCmd)
	assert.NotEmpty(t, branchCmd.Use)
}

func TestRootCmdHasVersionFlag(t *testing.T) {
	assert.NotNil(t, rootCmd)
	assert.Equal(t, version, rootCmd.Version)
}
