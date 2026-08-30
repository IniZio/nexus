package cli

import (
	"strings"
	"testing"

	mcp "github.com/IniZio/nexus3/internal/mcp"
)

// surfaceEntry documents one CLI verb's canonical API backing and MCP tools.
// This table is the authoritative surface contract: a CLI verb absent from
// this table means N-AC4 is violated (behaviour unreachable from canonical API).
type surfaceEntry struct {
	// CLIVerb is the name registered in cli.Register() via init().
	CLIVerb string
	// CanonicalMethods names the service.* functions or types this verb uses.
	// Informational; not verified at runtime — the table's presence is the assertion.
	CanonicalMethods []string
	// MCPTools lists the MCP tool names that provide the same operation.
	// Empty means no MCP equivalent (CLI-only).
	MCPTools []string
	// CLIOnly marks verbs that are legitimately CLI-only (no service backing,
	// no N-AC4 violation). Examples: doctor, version, auth.
	CLIOnly bool
}

// surfaceMap is the authoritative surface contract for N-AC4.
// Every registered CLI verb must appear here; every MCP tool must appear here.
// Update this table whenever a new verb or MCP tool is added — the test will
// fail at CI time if the table drifts from what is registered.
var surfaceMap = []surfaceEntry{
	// __herdr-plugin: deprecated alias, Hidden. Same canonical backing as `herdr`.
	{CLIVerb: "__herdr-plugin", CanonicalMethods: []string{"service.CreateAndBoot", "service.List", "service.Remove"}, MCPTools: nil},
	// herdr: plugin-private command group, Hidden. Dispatches all herdr-plugin verbs.
	{CLIVerb: "herdr", CanonicalMethods: []string{"service.CreateAndBoot", "service.List", "service.Remove"}, MCPTools: nil},
	{CLIVerb: "attach", CanonicalMethods: []string{"service.Exec"}, MCPTools: nil},
	{CLIVerb: "auth", CLIOnly: true},
	// config-ssh writes a local ~/.ssh/config stanza; the service is only used
	// to resolve the sandbox ref — the write itself is a CLI-local operation.
	{CLIVerb: "config-ssh", CanonicalMethods: []string{"service.SSHConn"}, MCPTools: nil},
	{CLIVerb: "cp", CanonicalMethods: []string{"service.Copy"}, MCPTools: nil},
	{CLIVerb: "doctor", CLIOnly: true},
	{CLIVerb: "egress", CanonicalMethods: []string{"service.ResolveRef"}, MCPTools: nil},
	{CLIVerb: "exec", CanonicalMethods: []string{"service.Exec"}, MCPTools: []string{"sandbox_exec"}},
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
	// Flat lifecycle verbs (cmd_flat_verbs.go). Each delegates to runSandbox
	// with the grouped subcommand prepended, so it is backed by exactly the
	// same canonical method as the `sandbox` row above — not a second path.
	// MCPTools is nil because the MCP surface keeps the grouped tool names.
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
	// supervisor-upgrade: replaces a running sandbox's supervisor with the
	// current binary without rebooting the guest (motive
	// nexus3-host-supervisor-hotswap, slice 07). Uses service.ResolveRef to
	// find the sandbox and service.SetSupervisor to persist the replacement's
	// identity; the handoff itself is driven by supervisor.RequestHandoff /
	// supervisor.SpawnAdoptDetached, not a service.* method. CLI-only: no MCP
	// tool exposes a planned supervisor swap.
	{CLIVerb: "supervisor-upgrade", CanonicalMethods: []string{"service.ResolveRef", "service.SetSupervisor"}, MCPTools: nil},
	// supervisor-backfill-netns-identity: reconstructs and persists the netns
	// identity for a sandbox created before slice 04, so supervisor-upgrade
	// can adopt it (ticket 11). Uses service.ResolveRef to find the sandbox
	// and service.SetNetnsIdentity to persist the verified reconstruction;
	// the reconstruction itself is driven by
	// supervisor.BackfillNetnsIdentity, not a service.* method. CLI-only:
	// deliberately a separate, explicit verb from supervisor-upgrade rather
	// than a flag — see the ticket's Decision — so no MCP tool exposes it.
	{CLIVerb: "supervisor-backfill-netns-identity", CanonicalMethods: []string{"service.ResolveRef", "service.SetNetnsIdentity"}, MCPTools: nil},
	{CLIVerb: "version", CLIOnly: true},
	// volume noun manages named volumes directly via volumestore (not through
	// service.*) — the canonical backing is the VolumeStore CRUD primitives.
	{CLIVerb: "volume", CanonicalMethods: []string{"volumestore.Create", "volumestore.List", "volumestore.Rm", "volumestore.Prune"}, MCPTools: nil},
	// sandbox agent-upgrade: hot-swaps the in-guest binary via agent.AgentUpgrade
	// (which calls Copy + RestartAgent + AgentInfo RPCs).  CLI-only: no MCP tool.
	{CLIVerb: "sandbox agent-upgrade", CanonicalMethods: []string{"agent.AgentUpgrade", "agent.AgentInfo"}, MCPTools: nil},
}

// TestSurfaceParity verifies that every registered CLI verb and every MCP tool
// is covered by the surfaceMap contract (N-AC4: no behaviour unreachable from
// the canonical API).
func TestSurfaceParity(t *testing.T) {
	// Build lookup maps from the authoritative table.
	tableByVerb := make(map[string]surfaceEntry, len(surfaceMap))
	tableByTool := make(map[string]string) // tool name → CLI verb
	for _, e := range surfaceMap {
		tableByVerb[e.CLIVerb] = e
		for _, tool := range e.MCPTools {
			tableByTool[tool] = e.CLIVerb
		}
	}

	var drift []string

	// 1. Every registered CLI verb must be in the table.
	for _, cmd := range All() {
		if _, ok := tableByVerb[cmd.Name]; !ok {
			drift = append(drift, "CLI verb "+cmd.Name+" has no surface-contract entry (N-AC4 violation)")
		}
	}

	// 2. Every table entry's CLI verb should be actually registered.
	//    Unregistered entries are warnings (forward-compat), not failures.
	registeredVerbs := make(map[string]bool)
	for _, cmd := range All() {
		registeredVerbs[cmd.Name] = true
	}
	for _, e := range surfaceMap {
		if !registeredVerbs[e.CLIVerb] {
			t.Logf("WARNING: surface table entry %q has no registered CLI command (stale entry?)", e.CLIVerb)
		}
	}

	// 3. Every MCP tool must be in the table.
	for _, tool := range mcp.KnownTools() {
		if _, ok := tableByTool[tool]; !ok {
			drift = append(drift, "MCP tool "+tool+" has no surface-contract entry (N-AC4 violation)")
		}
	}

	// 4. Every MCP tool listed in the table must exist in KnownTools.
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
