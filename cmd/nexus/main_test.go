package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionCmd(t *testing.T) {
	err := versionCmd.RunE(versionCmd, []string{})
	assert.NoError(t, err)
}

func TestRootCmdExists(t *testing.T) {
	assert.NotNil(t, rootCmd)
	assert.Equal(t, "nexus", rootCmd.Use)
}

func TestInitCmdExists(t *testing.T) {
	assert.NotNil(t, initCmd)
	assert.Equal(t, "init", initCmd.Use)
}

func TestBranchCmdExists(t *testing.T) {
	assert.NotNil(t, branchCmd)
	assert.Equal(t, "branch", branchCmd.Use)
}

func TestBranchCreateCmdExists(t *testing.T) {
	assert.NotNil(t, branchCreateCmd)
	assert.Equal(t, "create <name>", branchCreateCmd.Use)
	assert.NoError(t, branchCreateCmd.Args(nil, []string{"test"}))
}

func TestBranchUpCmdExists(t *testing.T) {
	assert.NotNil(t, branchUpCmd)
	assert.Equal(t, "up [name]", branchUpCmd.Use)
}

func TestBranchDownCmdExists(t *testing.T) {
	assert.NotNil(t, branchDownCmd)
	assert.Equal(t, "down [name]", branchDownCmd.Use)
}

func TestBranchListCmdExists(t *testing.T) {
	assert.NotNil(t, branchListCmd)
	assert.Equal(t, "list", branchListCmd.Use)
}

func TestBranchRmCmdExists(t *testing.T) {
	assert.NotNil(t, branchRmCmd)
	assert.Equal(t, "rm <name>", branchRmCmd.Use)
}

func TestCoordinationCmdExists(t *testing.T) {
	assert.NotNil(t, coordinationCmd)
	assert.Equal(t, "coordination", coordinationCmd.Use)
}

func TestCoordinationStartCmdExists(t *testing.T) {
	assert.NotNil(t, coordinationStartCmd)
	assert.Equal(t, "start", coordinationStartCmd.Use)
}

func TestNodeCmdExists(t *testing.T) {
	assert.NotNil(t, nodeCmd)
	assert.Equal(t, "node", nodeCmd.Use)
}

func TestNodeAddCmdExists(t *testing.T) {
	assert.NotNil(t, nodeAddCmd)
	assert.Equal(t, "add <name> <host>", nodeAddCmd.Use)
}

func TestNodeListCmdExists(t *testing.T) {
	assert.NotNil(t, nodeListCmd)
	assert.Equal(t, "list", nodeListCmd.Use)
}

func TestSSHCmdExists(t *testing.T) {
	assert.NotNil(t, sshCmd)
	assert.Equal(t, "ssh", sshCmd.Use)
}

func TestSSHGenerateCmdExists(t *testing.T) {
	assert.NotNil(t, sshGenerateCmd)
	assert.Equal(t, "generate", sshGenerateCmd.Use)
}

func TestPluginCmdExists(t *testing.T) {
	assert.NotNil(t, pluginCmd)
	assert.Equal(t, "plugin", pluginCmd.Use)
}

func TestConfigCmdExists(t *testing.T) {
	assert.NotNil(t, configCmd)
	assert.Equal(t, "config", configCmd.Use)
}

func TestApplyCmdExists(t *testing.T) {
	assert.NotNil(t, applyCmd)
	assert.Equal(t, "apply", applyCmd.Use)
}

func TestUpdateCmdExists(t *testing.T) {
	assert.NotNil(t, updateCmd)
	assert.Equal(t, "update", updateCmd.Use)
}

func TestUsageCmdExists(t *testing.T) {
	assert.NotNil(t, usageCmd)
	assert.Equal(t, "usage", usageCmd.Use)
}

func TestBranchShellCmdExists(t *testing.T) {
	assert.NotNil(t, branchShellCmd)
	assert.Equal(t, "shell [name]", branchShellCmd.Use)
}

func TestBranchConnectCmdExists(t *testing.T) {
	assert.NotNil(t, branchConnectCmd)
	assert.Equal(t, "connect <name>", branchConnectCmd.Use)
}

func TestBranchServicesCmdExists(t *testing.T) {
	assert.NotNil(t, branchServicesCmd)
	assert.Equal(t, "services <name>", branchServicesCmd.Use)
}

func TestSSHRegisterCmdExists(t *testing.T) {
	assert.NotNil(t, sshRegisterCmd)
	assert.Equal(t, "register <server>", sshRegisterCmd.Use)
}

func TestSSHInfoCmdExists(t *testing.T) {
	assert.NotNil(t, sshInfoCmd)
	assert.Equal(t, "info <branch>", sshInfoCmd.Use)
}

func TestPluginUpdateCmdExists(t *testing.T) {
	assert.NotNil(t, pluginUpdateCmd)
	assert.Equal(t, "update", pluginUpdateCmd.Use)
}

func TestPluginListCmdExists(t *testing.T) {
	assert.NotNil(t, pluginListCmd)
	assert.Equal(t, "list", pluginListCmd.Use)
}

func TestConfigExtractCmdExists(t *testing.T) {
	assert.NotNil(t, configExtractCmd)
	assert.Equal(t, "extract <plugin-name>", configExtractCmd.Use)
}

func TestUsageSummaryCmdExists(t *testing.T) {
	assert.NotNil(t, usageSummaryCmd)
	assert.Equal(t, "summary [date]", usageSummaryCmd.Use)
}

func TestUsageMetricsCmdExists(t *testing.T) {
	assert.NotNil(t, usageMetricsCmd)
	assert.Equal(t, "metrics [days]", usageMetricsCmd.Use)
}

func TestUsagePatternsCmdExists(t *testing.T) {
	assert.NotNil(t, usagePatternsCmd)
	assert.Equal(t, "patterns [days]", usagePatternsCmd.Use)
}

func TestUsageBenchmarkCmdExists(t *testing.T) {
	assert.NotNil(t, usageBenchmarkCmd)
	assert.Equal(t, "benchmark <baseline-days> <current-days>", usageBenchmarkCmd.Use)
}
