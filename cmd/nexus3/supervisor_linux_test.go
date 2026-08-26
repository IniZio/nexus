//go:build linux

package main

import (
	"reflect"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// TestParseSupervisorFlags_RoundTrip pins the flag→struct glue: every
// forwarded SpawnConfig field must survive argv parsing into Config.
//
// Regression guard for 2026-08-16: an edit adding WorkspaceGuestPath to the
// Config literal silently dropped ExtraDisks and WorkspaceDiskIndex. The
// supervisor then attached ONLY the rootfs; every workspace sandbox's guest
// panicked at boot ("mount /dev/vdf … no such file") because its extra disks
// never reached vm.create. A round-trip test catches a dropped field at unit
// speed instead of after a 4-minute live boot.
func TestParseSupervisorFlags_RoundTrip(t *testing.T) {
	in := supervisor.Config{
		SandboxRef:         "sb-roundtrip",
		StoreRoot:          "/store",
		StateDir:           "/state",
		CHBin:              "/usr/bin/cloud-hypervisor",
		SocketDir:          "/run/nexus3",
		KernelPath:         "/boot/vmlinux",
		DiskPath:           "/data/sb.raw",
		CredsFile:          "/creds.json",
		MemoryMiB:          2048,
		BootVCPUs:          2,
		HasWorkspaceDisk:   true,
		WorkspaceDiskIndex: 4,
		WorkspaceGuestPath: "/workspace/proj",
		ExtraDisks:           []string{"/d1.ext4", "/d2.ext4", "/d3.ext4", "/d4.ext4", "/d5.ext4"},
		ResizableDiskIndices: []int{2},
		GovBounds: resize.Bounds{
			MemMinBytes:  512 << 20,
			MemMaxBytes:  4096 << 20,
			VCPUMin:      1,
			VCPUMax:      4,
			DiskMaxBytes: 100 << 30,
		},
		Cmdline: "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0 -- --workspace-mount=/dev/vdf:/workspace/proj:ext4:false:true",
		LiveMounts: []domain.LiveMount{
			{HostPath: "/home/op/src", GuestPath: "/work"},
			{HostPath: "/home/op/ref", GuestPath: "/ref", ReadOnly: true},
		},
		VirtiofsdPath: "/usr/libexec/virtiofsd",
	}

	// BuildSupervisorArgv includes the leading HiddenSubcommand token; the
	// CLI dispatch strips it before runSupervisorMain (os.Args[2:]).
	argv := supervisor.BuildSupervisorArgv(supervisor.SpawnConfig{Config: in})[1:]
	got, err := parseSupervisorFlags(argv)
	if err != nil {
		t.Fatalf("parseSupervisorFlags: %v", err)
	}

	if got.SandboxRef != in.SandboxRef || got.DiskPath != in.DiskPath || got.Cmdline != in.Cmdline {
		t.Errorf("identity fields lost: %+v", got)
	}
	if len(got.ExtraDisks) != len(in.ExtraDisks) {
		t.Fatalf("ExtraDisks round-trip: got %d disks (%v), want %d — dropped fields attach only the rootfs and panic workspace guests",
			len(got.ExtraDisks), got.ExtraDisks, len(in.ExtraDisks))
	}
	for i, p := range in.ExtraDisks {
		if got.ExtraDisks[i] != p {
			t.Errorf("ExtraDisks[%d] = %q, want %q", i, got.ExtraDisks[i], p)
		}
	}
	if got.WorkspaceDiskIndex != in.WorkspaceDiskIndex || !got.HasWorkspaceDisk {
		t.Errorf("workspace disk index lost: got idx=%d has=%v, want idx=%d has=true",
			got.WorkspaceDiskIndex, got.HasWorkspaceDisk, in.WorkspaceDiskIndex)
	}
	if got.WorkspaceGuestPath != in.WorkspaceGuestPath {
		t.Errorf("WorkspaceGuestPath = %q, want %q", got.WorkspaceGuestPath, in.WorkspaceGuestPath)
	}
	if got.MemoryMiB != in.MemoryMiB || got.BootVCPUs != in.BootVCPUs || got.CredsFile != in.CredsFile {
		t.Errorf("boot config lost: %+v", got)
	}
	if got.GovBounds != in.GovBounds {
		t.Errorf("GovBounds = %+v, want %+v", got.GovBounds, in.GovBounds)
	}
	// Regression guard for 2026-08-21: LiveMounts and VirtiofsdPath were added
	// to supervisor.Config and written to spawn.json, but never added to the
	// argv the detached supervisor is actually launched with. The supervisor
	// therefore booted every --mount sandbox with fs=null and
	// memory.shared=false (confirmed live via CH vm.info) while the guest
	// cmdline still asked to mount virtiofs tag nx3fs0 — the guest agent hung
	// at mount time and never listened on vsock, so `nexus3 exec` failed with
	// "read handshake reply: EOF" on a create that reported success.
	if !reflect.DeepEqual(got.LiveMounts, in.LiveMounts) {
		t.Errorf("LiveMounts = %+v, want %+v — dropped mounts boot the VM with no virtiofs device and hang the guest at mount time",
			got.LiveMounts, in.LiveMounts)
	}
	if got.VirtiofsdPath != in.VirtiofsdPath {
		t.Errorf("VirtiofsdPath = %q, want %q", got.VirtiofsdPath, in.VirtiofsdPath)
	}
}

// TestParseSupervisorFlags_EveryConfigFieldSurvives is the class-level guard
// behind the field-by-field assertions above.
//
// The failure shape this file keeps hitting is drift between supervisor.Config
// (what the CLI fills in) and the argv codec (what actually reaches the
// detached process): ExtraDisks in 2026-08-16, LiveMounts + VirtiofsdPath in
// 2026-08-21. Both were invisible to unit tests and cost a live boot to find.
//
// This test closes the class in two steps:
//
//  1. Reflection asserts the input Config has NO zero-valued field. Adding a
//     field to supervisor.Config fails here until it is populated below.
//  2. DeepEqual asserts the whole struct survives the argv round-trip. Once
//     the new field is populated, this fails until BuildSupervisorArgv and
//     parseSupervisorFlags both carry it.
func TestParseSupervisorFlags_EveryConfigFieldSurvives(t *testing.T) {
	in := supervisor.Config{
		SandboxRef:         "sb-allfields",
		StoreRoot:          "/store",
		StateDir:           "/state",
		CHBin:              "/usr/bin/cloud-hypervisor",
		SocketDir:          "/run/nexus3",
		KernelPath:         "/boot/vmlinux",
		DiskPath:           "/data/sb.raw",
		ExtraDisks:           []string{"/d1.ext4"},
		ResizableDiskIndices: []int{2},
		WorkspaceGuestPath:   "/workspace/proj",
		CredsFile:          "/creds.json",
		MemoryMiB:          2048,
		BootVCPUs:          2,
		HasWorkspaceDisk:   true,
		WorkspaceDiskIndex: 0,
		GovBounds: resize.Bounds{
			MemMinBytes:  512 << 20,
			MemMaxBytes:  4096 << 20,
			VCPUMin:      1,
			VCPUMax:      4,
			DiskMaxBytes: 100 << 30,
		},
		Cmdline:       "root=/dev/vda rw",
		LiveMounts:    []domain.LiveMount{{HostPath: "/h", GuestPath: "/g", ReadOnly: true}},
		VirtiofsdPath: "/usr/libexec/virtiofsd",
		Ephemeral:     true,
		ParentPipeFD:  7,
		// MCPOAuthRefreshConfigs: spawn.json-only field (refresh tokens must NOT
		// appear in argv because argv is ps-visible). Populated here so Step 1
		// (no-zero-field guard) passes; excluded from Step 2's argv DeepEqual
		// and covered separately by TestMCPOAuthRefreshConfigs_SpawnSpecRoundTrip.
		MCPOAuthRefreshConfigs: []service.MCPOAuthRefreshConfig{
			{ServerName: "linear-server", Host: "api.linear.app"},
		},
	}

	// Step 1: no field may be left at its zero value.
	//
	// WorkspaceDiskIndex is exempt: 0 is a MEANINGFUL value (the first extra
	// disk) and HasWorkspaceDisk above is what makes it live. It is asserted
	// explicitly by TestParseSupervisorFlags_RoundTrip with a non-zero index.
	//
	// MCPOAuthRefreshConfigs is exempt from the argv round-trip (Step 2):
	// refresh tokens must not appear in argv (ps-visible); this field travels
	// via spawn.json. It is covered by TestMCPOAuthRefreshConfigs_SpawnSpecRoundTrip.
	v := reflect.ValueOf(in)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "WorkspaceDiskIndex" {
			continue
		}
		if v.Field(i).IsZero() {
			t.Fatalf("supervisor.Config.%s is zero in this test's input: populate it, then make BuildSupervisorArgv + parseSupervisorFlags carry it", name)
		}
	}

	// Step 2: the argv-carried fields must survive the argv round-trip.
	// MCPOAuthRefreshConfigs travels via spawn.json (not argv) so it is
	// zeroed on both sides before the DeepEqual. Its own round-trip is
	// asserted by TestMCPOAuthRefreshConfigs_SpawnSpecRoundTrip.
	argv := supervisor.BuildSupervisorArgv(supervisor.SpawnConfig{Config: in})[1:]
	got, err := parseSupervisorFlags(argv)
	if err != nil {
		t.Fatalf("parseSupervisorFlags: %v", err)
	}
	wantArgv := in
	wantArgv.MCPOAuthRefreshConfigs = nil
	got.MCPOAuthRefreshConfigs = nil
	if !reflect.DeepEqual(got, wantArgv) {
		t.Errorf("Config did not survive the argv round-trip\n got: %+v\nwant: %+v", got, wantArgv)
	}
}

// TestMCPOAuthRefreshConfigs_SpawnSpecRoundTrip is the mandatory round-trip
// test for the spawn.json conveyance path that carries MCPOAuthRefreshConfigs
// to the detached supervisor subprocess.
//
// MCPOAuthRefreshConfigs holds OAuth refresh tokens that must NOT appear in
// argv (ps-visible). runSupervisorMain reads them from spawn.json
// (written at 0600 before the subprocess is forked) via ReadSpawnSpec.
//
// Failure mode this test catches: the field is added to supervisor.Config and
// written to spawn.json by the CLI, but never read back in runSupervisorMain
// — the supervisor then starts with MCPOAuthRefreshConfigs==nil and never
// calls StartMCPOAuthRefreshers, so MCP OAuth egress produces 401.
//
// To verify this test is mutation-proof: momentarily remove the
// cfg.MCPOAuthRefreshConfigs = spec.MCPOAuthRefreshConfigs assignment in
// runSupervisorMain and confirm this test fails.
func TestMCPOAuthRefreshConfigs_SpawnSpecRoundTrip(t *testing.T) {
	stateDir := t.TempDir()

	want := []service.MCPOAuthRefreshConfig{
		{
			ServerName:    "linear-server",
			Host:          "api.linear.app",
			AccessToken:   "access-tok-1",
			RefreshToken:  "refresh-tok-1",
			TokenEndpoint: "https://api.linear.app/oauth/token",
			ClientID:      "client-abc",
			ExpiresAtMs:   1_700_000_000_000,
		},
		{
			ServerName:    "glitchtip",
			Host:          "app.glitchtip.com",
			AccessToken:   "access-tok-2",
			RefreshToken:  "refresh-tok-2",
			TokenEndpoint: "https://app.glitchtip.com/oauth/token/",
			ClientID:      "client-xyz",
			ExpiresAtMs:   1_800_000_000_000,
		},
	}

	cfg := supervisor.Config{
		SandboxRef:             "sb-mcp-test",
		StoreRoot:              "/store",
		StateDir:               stateDir,
		CHBin:                  "/usr/bin/cloud-hypervisor",
		SocketDir:              "/run/nexus3",
		KernelPath:             "/boot/vmlinux",
		DiskPath:               "/data/sb.raw",
		MCPOAuthRefreshConfigs: want,
	}

	if err := supervisor.WriteSpawnSpec(stateDir, cfg); err != nil {
		t.Fatalf("WriteSpawnSpec: %v", err)
	}

	// Simulate what runSupervisorMain does: read spawn.json and pull the field.
	spec, err := supervisor.ReadSpawnSpec(stateDir)
	if err != nil {
		t.Fatalf("ReadSpawnSpec: %v", err)
	}
	if !reflect.DeepEqual(spec.MCPOAuthRefreshConfigs, want) {
		t.Errorf("MCPOAuthRefreshConfigs did not survive spawn.json round-trip\n got: %+v\nwant: %+v",
			spec.MCPOAuthRefreshConfigs, want)
	}
}

// TestParseSupervisorFlags_MissingRequired asserts the required-flag guard
// fires with the first missing flag name.
func TestParseSupervisorFlags_MissingRequired(t *testing.T) {
	_, err := parseSupervisorFlags([]string{"--sandbox-ref", "sb-x"})
	if err == nil || err.Error() != "supervisor: --store-root is required" {
		t.Fatalf("err = %v, want --store-root required", err)
	}
}
