package cli

import (
	"strings"
	"testing"

	mcp "github.com/IniZio/nexus3/internal/mcp"
)

// surfaceEntry documents one CLI verb's canonical API backing and MCP tools.
type surfaceEntry struct {
	CLIVerb          string
	CanonicalMethods []string
	MCPTools         []string
	CLIOnly          bool
}

// surfaceMap is the authoritative surface contract for N-AC4.
var surfaceMap = []surfaceEntry{
	{CLIVerb: "__herdr-plugin", CanonicalMethods: []string{"service.CreateAndBoot", "service.List", "service.Remove"}, MCPTools: nil},
	{CLIVerb: "herdr", CanonicalMethods: []string{"service.CreateAndBoot", "service.List", "service.Remove"}, MCPTools: []string{"delegate_worktree_create", "delegate_agent_dispatch", "delegate_teardown"}},
	{CLIVerb: "attach", CanonicalMethods: []string{"service.Exec"}, MCPTools: nil},
	{CLIVerb: "auth", CLIOnly: true},
	{CLIVerb: "config validate", CLIOnly: true},
	{CLIVerb: "config-ssh", CanonicalMethods: []string{"service.SSHConn"}, MCPTools: nil},
	{CLIVerb: "cp", CanonicalMethods: []string{"service.Copy"}, MCPTools: nil},
	{CLIVerb: "disk", CanonicalMethods: []string{"service.DiskUsage"}, MCPTools: nil},
	{CLIVerb: "doctor", CLIOnly: true},
	{CLIVerb: "egress", CanonicalMethods: []string{"service.ResolveRef"}, MCPTools: nil},
	{CLIVerb: "exec", CanonicalMethods: []string{"service.Exec"}, MCPTools: []string{"sandbox_exec", "delegate_agent_poll"}},
	{CLIVerb: "fork", CanonicalMethods: []string{"service.Fork"}, MCPTools: nil},
	{CLIVerb: "forward", CanonicalMethods: []string{"service.Forward"}, MCPTools: nil},
	{CLIVerb: "harvest", CanonicalMethods: []string{"service.Harvest"}, MCPTools: nil},
	{CLIVerb: "image", CanonicalMethods: []string{"service.ImageOps"}, MCPTools: nil},
	{CLIVerb: "log", CanonicalMethods: []string{"service.ResolveRef"}, MCPTools: nil},
	{CLIVerb: "mcp", CanonicalMethods: []string{"service.*"}, MCPTools: nil},
	{CLIVerb: "orca", CanonicalMethods: []string{"service.CreateAndBoot", "service.List"}, MCPTools: nil},
	{CLIVerb: "reap", CanonicalMethods: []string{"service.Reap"}, MCPTools: nil},
	{CLIVerb: "recover", CanonicalMethods: []string{"service.Recover"}, MCPTools: nil},
	{CLIVerb: "restore", CanonicalMethods: []string{"service.RestoreFromSnapshot"}, MCPTools: nil},
	{CLIVerb: "run", CanonicalMethods: []string{"service.RunEphemeral"}, MCPTools: []string{"sandbox_run"}},
	{CLIVerb: "sandbox", CanonicalMethods: []string{"service.Create", "service.List", "service.Start", "service.Stop", "service.Pause", "service.Resume", "service.Remove"}, MCPTools: []string{"sandbox_create", "sandbox_list", "sandbox_start", "sandbox_stop", "sandbox_pause", "sandbox_resume", "sandbox_remove"}},
	{CLIVerb: "create", CanonicalMethods: []string{"service.CreateAndBoot"}, MCPTools: nil},
	{CLIVerb: "ps", CanonicalMethods: []string{"service.List"}, MCPTools: nil},
	{CLIVerb: "ls", CanonicalMethods: []string{"service.List"}, MCPTools: nil},
	{CLIVerb: "rm", CanonicalMethods: []string{"service.Remove"}, MCPTools: nil},
	{CLIVerb: "start", CanonicalMethods: []string{"service.Start"}, MCPTools: nil},
	{CLIVerb: "stop", CanonicalMethods: []string{"service.Stop"}, MCPTools: nil},
	{CLIVerb: "pause", CanonicalMethods: []string{"service.Pause"}, MCPTools: nil},
	{CLIVerb: "resume", CanonicalMethods: []string{"service.Resume"}, MCPTools: nil},
	{CLIVerb: "shell", CanonicalMethods: []string{"service.Exec"}, MCPTools: nil},
	{CLIVerb: "snapshot", CanonicalMethods: []string{"service.Snapshot", "service.SnapshotList", "service.SnapshotRemove"}, MCPTools: nil},
	{CLIVerb: "ssh", CanonicalMethods: []string{"service.SSHConn"}, MCPTools: nil},
	{CLIVerb: "supervisor-upgrade", CanonicalMethods: []string{"service.ResolveRef", "service.SetSupervisor"}, MCPTools: nil},
	{CLIVerb: "supervisor-backfill-netns-identity", CanonicalMethods: []string{"service.ResolveRef", "service.SetNetnsIdentity"}, MCPTools: nil},
	{CLIVerb: "version", CLIOnly: true},
	{CLIVerb: "volume", CanonicalMethods: []string{"volumestore.Create", "volumestore.List", "volumestore.Rm", "volumestore.Prune"}, MCPTools: nil},
	{CLIVerb: "sandbox agent-upgrade", CanonicalMethods: []string{"agent.AgentUpgrade", "agent.AgentInfo"}, MCPTools: nil},
}

// TestSurfaceParity verifies that every registered CLI verb and every MCP tool is covered by the surfaceMap contract (N-AC4).
func TestSurfaceParity(t *testing.T) {
	tableByVerb := make(map[string]surfaceEntry, len(surfaceMap))
	tableByTool := make(map[string]string)
	for _, e := range surfaceMap {
		tableByVerb[e.CLIVerb] = e
		for _, tool := range e.MCPTools {
			tableByTool[tool] = e.CLIVerb
		}
	}

	var drift []string

	for _, cmd := range All() {
		if _, ok := tableByVerb[cmd.Name]; !ok {
			drift = append(drift, "CLI verb "+cmd.Name+" has no surface-contract entry (N-AC4 violation)")
		}
	}

	registeredVerbs := make(map[string]bool)
	for _, cmd := range All() {
		registeredVerbs[cmd.Name] = true
	}
	for _, e := range surfaceMap {
		if !registeredVerbs[e.CLIVerb] {
			t.Logf("WARNING: surface table entry %q has no registered CLI command (stale entry?)", e.CLIVerb)
		}
	}

	for _, tool := range mcp.KnownTools() {
		if _, ok := tableByTool[tool]; !ok {
			drift = append(drift, "MCP tool "+tool+" has no surface-contract entry (N-AC4 violation)")
		}
	}

	knownTools := make(map[string]bool)
	for _, tool := range mcp.KnownTools() {
		knownTools[tool] = true
	}
	for _, e := range surfaceMap {
		for _, tool := range e.MCPTools {
			if !knownTools[tool] {
				drift = append(drift, "surface table MCP tool "+tool+" not registered by mcp.KnownTools()")
			}
		}
	}

	if len(drift) > 0 {
		t.Errorf("Surface parity violations found (%d):\n%s\nFix: update surfaceMap in surface_parity_test.go or add missing canonical-API backing.", len(drift), strings.Join(drift, "\n"))
	}
}
