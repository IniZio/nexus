package cli

import (
	"reflect"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/resize"
)

// knownOptionalSupervisorConfigFields lists supervisor.Config fields that may
// legitimately be zero for a fully-specified agent+workspace+mount sandbox.
// Each entry carries an explanation so a future reader can tell a deliberate
// exemption from an oversight.
//
// If you add a new field here you must also explain why its zero value is
// correct for a persistent human sandbox — "I'm not sure" is not a reason.
var knownOptionalSupervisorConfigFields = map[string]string{
	// Ephemeral: false is correct for a persistent human sandbox. The builder
	// path (cmd_herdr_plugin.go, builder_supervisor_driver.go) sets it to true;
	// the human create path must leave it false.
	"Ephemeral": "false = non-ephemeral persistent human sandbox; builder mode sets true",
	// ParentPipeFD: 0 means no watchdog pipe. The pipe is only used in
	// ephemeral (builder) mode so the supervisor exits when the CLI is
	// SIGKILL'd. Persistent supervisors are intentionally long-lived after
	// CLI exit and do not use the pipe.
	"ParentPipeFD": "0 = no watchdog pipe; only ephemeral supervisors use it",
	// MCPOAuthRefreshConfigs: nil is correct when no OAuth MCP servers are
	// configured. The field is populated only when BuildMCPOAuthBinds finds
	// OAuth entries in ~/.claude/.credentials.json; its absence is never a bug.
	"MCPOAuthRefreshConfigs": "nil = no OAuth MCP servers; populated on demand by BuildMCPOAuthBinds",
	// CacheDiskSlots / CacheDiskLeaseFDs: nil is correct for a human sandbox.
	// Builder cache-disk slots exist only for the ephemeral builder VM that
	// `sandbox create --file` boots; a persistent human sandbox attaches no
	// cache disk and therefore leases no slot (D-HSH-07). The builder path
	// sets both — see supervisorBuilderDriver.buildSpawnConfig, guarded by
	// TestBuilderSupervisorDriver_HandsCacheDiskLeasesToTheSupervisor.
	"CacheDiskSlots":    "nil = no builder cache disk on a human sandbox; builder mode sets it",
	"CacheDiskLeaseFDs": "nil = no inherited lease descriptors; builder mode sets it via SpawnDetached",
}

// TestBuildHumanSupervisorConfig_AllFieldsPopulated verifies that
// buildHumanSupervisorConfig populates every field of supervisor.Config for a
// fully-specified agent+workspace+mount sandbox. It is the construction-site
// counterpart of TestParseSupervisorFlags_EveryConfigFieldSurvives (which
// guards the argv codec one layer below).
//
// MUTATION PROOF: deleting any non-exempt field assignment in
// buildHumanSupervisorConfig causes this test to fail with a message naming
// the dropped field. The test is the class guard, not just a spot check —
// adding a new field to supervisor.Config and forgetting to wire it here will
// also fail, because the new field's zero value will not appear in the exempt
// map and IsZero() will return true.
//
// The only gap: a new field whose correct value IS the zero value for its type
// (e.g. a bool that should always be false) must be added to
// knownOptionalSupervisorConfigFields with an explanatory comment to prevent a
// false alarm.
func TestBuildHumanSupervisorConfig_AllFieldsPopulated(t *testing.T) {
	// Pin the creds path so the test is deterministic and doesn't depend on $HOME.
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "/fake/creds.json")

	// Representative "agent + workspace + mount" configuration.
	// Every argument is intentionally non-zero:
	//   - WorkspaceDiskIndex 1, not 0: makes the int non-zero so a forgotten
	//     assignment is not masked by the Go zero value matching the first-disk
	//     case (index 0 == Go zero value == "forgotten" — ambiguous without this).
	//   - GovBounds: all subfields non-zero (active auto-resize governor).
	cfg := buildHumanSupervisorConfig(
		"aabbccddeeff1122",        // sandboxRef
		"/store",                  // storeRoot
		"/store/supervisors/aabb", // stateDir
		"/kernel/vmlinux",         // kernelPath
		resize.Bounds{ // govBounds — all subfields non-zero
			MemMinBytes:  512 << 20,
			MemMaxBytes:  4096 << 20,
			VCPUMin:      1,
			VCPUMax:      4,
			DiskMaxBytes: 20 << 30,
		},
		2048, 2, // memoryMiB, bootVCPUs
		"/disks/sb.raw", // diskPath
		[]string{"/disks/shd.raw", "/disks/ws.raw"},              // extraDisks (non-nil)
		"root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0", // cmdline
		"/usr/bin/cloud-hypervisor",                              // chBin
		"/tmp/sockets",                                           // socketDir
		true,                                                     // hasWorkspace
		1,                                                        // workspaceDiskIndex (non-zero, = 1 shadow disk)
		1,                                                        // numNamedDisks (non-zero, = 1 docker named volume)
		"/workspace/proj",                                        // workspaceGuestPath
		true, 3,                                                  // hasScratchDisk, scratchDiskIndex (numNamedDisks+workspaceDiskIndex+1 = 1+1+1)
		[]domain.LiveMount{{HostPath: "/src", GuestPath: "/work"}}, // liveMounts
		"/usr/bin/virtiofsd", // virtiofsdPath
		true,                 // nestedVirt — non-zero for AllFieldsPopulated
		nil,                  // mcpOAuthRefreshConfigs (optional; nil = none)
		cred.AgentProfile{},  // agentProfile — zero value treated as claude-code
	)

	rv := reflect.ValueOf(cfg)
	rt := rv.Type()
	var zeroFields []string
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if _, exempt := knownOptionalSupervisorConfigFields[name]; exempt {
			continue
		}
		if rv.Field(i).IsZero() {
			zeroFields = append(zeroFields, name)
		}
	}
	if len(zeroFields) > 0 {
		t.Errorf("supervisor.Config fields are zero for an agent+workspace+mount sandbox — "+
			"buildHumanSupervisorConfig is missing assignments for: %v\n"+
			"If a field is intentionally zero in this shape, add it to "+
			"knownOptionalSupervisorConfigFields with an explanatory comment.",
			zeroFields)
	}
}

// TestBuildHumanSupervisorConfig_CredsFilePopulated is a named regression for
// J16: without CredsFile the detached supervisor skips the Refresher
// (supervisor.go gates on CredsFile != ""), freezing the OAuth token at create
// time. A long-running agent sandbox then dies at expiry with an opaque 401
// inside the guest and nothing in supervisor.log to explain it.
func TestBuildHumanSupervisorConfig_CredsFilePopulated(t *testing.T) {
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "/fake/creds.json")

	// Minimal config — only the fields needed for a no-mount, no-workspace
	// sandbox. CredsFile must be set regardless of mounts or workspace.
	cfg := buildHumanSupervisorConfig(
		"aabbccddeeff1122", "/store", "/store/supervisors/aabb",
		"/kernel/vmlinux",
		resize.Bounds{MemMinBytes: 1, MemMaxBytes: 2},
		512, 1,
		"/disks/sb.raw", nil,
		"root=/dev/vda rw", "/usr/bin/cloud-hypervisor", "/tmp/sockets",
		false, 0, 0, "",
		false, -1, // hasScratchDisk, scratchDiskIndex — no workspace, no scratch
		nil, "",
		false,               // nestedVirt
		nil,                 // mcpOAuthRefreshConfigs — optional
		cred.AgentProfile{}, // agentProfile — zero value treated as claude-code
	)
	if cfg.CredsFile == "" {
		t.Error("supervisor.Config.CredsFile is empty — the detached supervisor " +
			"will build no Refresher (supervisor.go gates on CredsFile != \"\"), " +
			"freezing the agent OAuth token at create time (J16)")
	}
	if cfg.CredsFile != "/fake/creds.json" {
		t.Errorf("CredsFile = %q, want /fake/creds.json", cfg.CredsFile)
	}
}

// TestBuildHumanSupervisorConfig_NamedDiskResizableIndices verifies that named
// kind=disk volume disks appear in ResizableDiskIndices at the correct absolute
// ExtraDisks indices, preceding the workspace disk index.
//
// MUTATION PROOF (index wiring): the docker volume index MUST appear in
// ResizableDiskIndices. This test fails if the named-disk loop in
// buildHumanSupervisorConfig is removed or the index formula is wrong.
func TestBuildHumanSupervisorConfig_NamedDiskResizableIndices(t *testing.T) {
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "/fake/creds.json")

	cases := []struct {
		name           string
		numNamedDisks  int
		numShadowDisks int // workspaceDiskIndex passed in (shadow count)
		hasWorkspace   bool
		wantResizable  []int
	}{
		{
			name:          "one docker disk no shadow no workspace",
			numNamedDisks: 1, numShadowDisks: 0, hasWorkspace: false,
			wantResizable: []int{0},
		},
		{
			name:          "one docker disk one shadow with workspace",
			numNamedDisks: 1, numShadowDisks: 1, hasWorkspace: true,
			// ExtraDisks: [docker(0), shadow(1), workspace(2)]
			// named: [0], workspace: 1+1=2
			wantResizable: []int{0, 2},
		},
		{
			name:          "no named disks with workspace",
			numNamedDisks: 0, numShadowDisks: 0, hasWorkspace: true,
			wantResizable: []int{0},
		},
		{
			name:          "two named disks no shadow with workspace",
			numNamedDisks: 2, numShadowDisks: 0, hasWorkspace: true,
			// ExtraDisks: [named0(0), named1(1), workspace(2)]
			// named: [0, 1], workspace: 2+0=2
			wantResizable: []int{0, 1, 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := buildHumanSupervisorConfig(
				"aabbccddeeff1122", "/store", "/store/supervisors/aabb",
				"/kernel/vmlinux",
				resize.Bounds{MemMinBytes: 1, MemMaxBytes: 2, DiskMaxBytes: 100 << 30},
				512, 1,
				"/disks/sb.raw", nil,
				"root=/dev/vda rw", "/usr/bin/cloud-hypervisor", "/tmp/sockets",
				tc.hasWorkspace, tc.numShadowDisks, tc.numNamedDisks, "",
				tc.hasWorkspace, tc.numNamedDisks+tc.numShadowDisks+1, // hasScratchDisk, scratchDiskIndex
				nil, "",
				false,               // nestedVirt
				nil,                 // mcpOAuthRefreshConfigs
				cred.AgentProfile{}, // agentProfile — zero value treated as claude-code
			)
			got := cfg.ResizableDiskIndices
			if len(got) != len(tc.wantResizable) {
				t.Fatalf("ResizableDiskIndices = %v, want %v", got, tc.wantResizable)
			}
			for i, idx := range tc.wantResizable {
				if got[i] != idx {
					t.Errorf("ResizableDiskIndices[%d] = %d, want %d (full: %v)", i, got[i], idx, got)
				}
			}
		})
	}
}
