package main

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
)

func TestParseWorkspaceMountArg_Valid(t *testing.T) {
	cases := []struct {
		arg     string
		wantDev string
		wantTgt string
		wantFS  string
		wantRO  bool
		wantWS  bool
	}{
		{
			// 4-field backward-compat format: IsWorkspace defaults to false.
			arg:     "--workspace-mount=/dev/vdf:/workspace/repo:ext4:false",
			wantDev: "/dev/vdf",
			wantTgt: "/workspace/repo",
			wantFS:  "ext4",
			wantRO:  false,
			wantWS:  false,
		},
		{
			// 4-field: shadow mount (node_modules), IsWorkspace=false.
			arg:     "--workspace-mount=/dev/vdb:/workspace/repo/node_modules:ext4:false",
			wantDev: "/dev/vdb",
			wantTgt: "/workspace/repo/node_modules",
			wantFS:  "ext4",
			wantRO:  false,
			wantWS:  false,
		},
		{
			// 4-field: ReadOnly=true, IsWorkspace=false.
			arg:     "--workspace-mount=/dev/vdc:/workspace/repo/.next:ext4:true",
			wantDev: "/dev/vdc",
			wantTgt: "/workspace/repo/.next",
			wantFS:  "ext4",
			wantRO:  true,
			wantWS:  false,
		},
		{
			// readonly field is anything other than "true" → false
			arg:     "--workspace-mount=/dev/vdb:/workspace/x:ext4:false",
			wantDev: "/dev/vdb",
			wantTgt: "/workspace/x",
			wantFS:  "ext4",
			wantRO:  false,
			wantWS:  false,
		},
		{
			// 5-field format: IsWorkspace=true (primary workspace disk).
			arg:     "--workspace-mount=/dev/vdf:/workspace/repo:ext4:false:true",
			wantDev: "/dev/vdf",
			wantTgt: "/workspace/repo",
			wantFS:  "ext4",
			wantRO:  false,
			wantWS:  true,
		},
		{
			// 5-field format: shadow mount, IsWorkspace=false.
			arg:     "--workspace-mount=/dev/vdb:/workspace/repo/node_modules:ext4:false:false",
			wantDev: "/dev/vdb",
			wantTgt: "/workspace/repo/node_modules",
			wantFS:  "ext4",
			wantRO:  false,
			wantWS:  false,
		},
		{
			// virtiofs: tag in device position, fstype=virtiofs, read-write workspace.
			arg:     "--workspace-mount=workspace-tag:/workspace/repo:virtiofs:false:true",
			wantDev: "workspace-tag",
			wantTgt: "/workspace/repo",
			wantFS:  "virtiofs",
			wantRO:  false,
			wantWS:  true,
		},
		{
			// virtiofs: read-only, not primary workspace (e.g. a shared read-only volume).
			arg:     "--workspace-mount=shared-data-tag:/workspace/shared:virtiofs:true:false",
			wantDev: "shared-data-tag",
			wantTgt: "/workspace/shared",
			wantFS:  "virtiofs",
			wantRO:  true,
			wantWS:  false,
		},
		{
			// virtiofs: 4-field backward-compat (IsWorkspace defaults false).
			arg:     "--workspace-mount=my-vol-tag:/workspace/repo:virtiofs:false",
			wantDev: "my-vol-tag",
			wantTgt: "/workspace/repo",
			wantFS:  "virtiofs",
			wantRO:  false,
			wantWS:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			got, ok := parseWorkspaceMountArg(tc.arg)
			if !ok {
				t.Fatalf("parseWorkspaceMountArg(%q) returned ok=false; want ok=true", tc.arg)
			}
			if got.Device != tc.wantDev {
				t.Errorf("Device: got %q, want %q", got.Device, tc.wantDev)
			}
			if got.Target != tc.wantTgt {
				t.Errorf("Target: got %q, want %q", got.Target, tc.wantTgt)
			}
			if got.FSType != tc.wantFS {
				t.Errorf("FSType: got %q, want %q", got.FSType, tc.wantFS)
			}
			if got.ReadOnly != tc.wantRO {
				t.Errorf("ReadOnly: got %v, want %v", got.ReadOnly, tc.wantRO)
			}
			if got.IsWorkspace != tc.wantWS {
				t.Errorf("IsWorkspace: got %v, want %v", got.IsWorkspace, tc.wantWS)
			}
		})
	}
}

func TestParseWorkspaceMountArg_Malformed(t *testing.T) {
	bad := []string{
		"",
		"--workspace-mount=",
		"--workspace-mount=onlyone",
		"--workspace-mount=/dev/vdb:/workspace/repo",      // only 2 fields
		"--workspace-mount=/dev/vdb:/workspace/repo:ext4", // only 3 fields
		"--cache-disk=/dev/vdb:/something",                // wrong prefix
		"--workspace-mount=:target:ext4:false",            // empty device
		"--workspace-mount=/dev/vdb::ext4:false",          // empty target
	}
	for _, arg := range bad {
		t.Run(arg, func(t *testing.T) {
			_, ok := parseWorkspaceMountArg(arg)
			if ok {
				t.Errorf("parseWorkspaceMountArg(%q) returned ok=true; want ok=false", arg)
			}
		})
	}
}

// TestParseWorkspaceMountArg_RoundTrip verifies that encoding a GuestMount in
// the host-side format (5 fields) and parsing it back in the guest produces the
// original value. This pins the host/guest contract without requiring KVM.
func TestParseWorkspaceMountArg_RoundTrip(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/repo/node_modules", FSType: "ext4", ReadOnly: false, IsWorkspace: false},
		{Device: "/dev/vdc", Target: "/workspace/repo/.next", FSType: "ext4", ReadOnly: false, IsWorkspace: false},
		{Device: "/dev/vdd", Target: "/workspace/repo/target", FSType: "ext4", ReadOnly: false, IsWorkspace: false},
		{Device: "/dev/vde", Target: "/workspace/repo/dist", FSType: "ext4", ReadOnly: false, IsWorkspace: false},
		{Device: "/dev/vdf", Target: "/workspace/repo", FSType: "ext4", ReadOnly: false, IsWorkspace: true},
	}

	for _, m := range mounts {
		ro := "false"
		if m.ReadOnly {
			ro = "true"
		}
		ws := "false"
		if m.IsWorkspace {
			ws = "true"
		}
		// Encode using the 5-field format emitted by workspaceMountCmdline.
		encoded := "--workspace-mount=" + m.Device + ":" + m.Target + ":" + m.FSType + ":" + ro + ":" + ws
		got, ok := parseWorkspaceMountArg(encoded)
		if !ok {
			t.Errorf("round-trip failed for %+v: parse returned ok=false", m)
			continue
		}
		if got != m {
			t.Errorf("round-trip mismatch for %+v: got %+v", m, got)
		}
	}
}

// TestParseWorkspaceMountArg_RoundTrip_Virtiofs verifies that the virtiofs
// variant of the host-emitted arg format round-trips through the guest parser,
// including the read-only variant. The virtiofs "device" position holds an
// opaque tag string rather than a /dev path; the parser must treat it as-is.
func TestParseWorkspaceMountArg_RoundTrip_Virtiofs(t *testing.T) {
	mounts := []agent.GuestMount{
		// read-write workspace virtiofs mount
		{Device: "workspace-tag", Target: "/workspace/repo", FSType: "virtiofs", ReadOnly: false, IsWorkspace: true},
		// read-only virtiofs (shared data volume)
		{Device: "shared-data-tag", Target: "/workspace/shared", FSType: "virtiofs", ReadOnly: true, IsWorkspace: false},
		// read-write shadow-style virtiofs at a nested path
		{Device: "shadow-vol-tag", Target: "/workspace/repo/node_modules", FSType: "virtiofs", ReadOnly: false, IsWorkspace: false},
	}

	for _, m := range mounts {
		ro := "false"
		if m.ReadOnly {
			ro = "true"
		}
		ws := "false"
		if m.IsWorkspace {
			ws = "true"
		}
		// Encode using the 5-field format emitted by workspaceMountCmdline.
		encoded := "--workspace-mount=" + m.Device + ":" + m.Target + ":" + m.FSType + ":" + ro + ":" + ws
		got, ok := parseWorkspaceMountArg(encoded)
		if !ok {
			t.Errorf("virtiofs round-trip failed for %+v: parse returned ok=false", m)
			continue
		}
		if got != m {
			t.Errorf("virtiofs round-trip mismatch for %+v: got %+v", m, got)
		}
	}
}
