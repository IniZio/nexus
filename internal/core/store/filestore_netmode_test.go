package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

const legacyTapRecord = `{"schema_version":1,"id":"sb-04106000000000000000000000","name":"tapbox","project":"proj","state":"stopped","envelope":{"ImageDigest":"","AllowedHosts":null,"SSHPublicKey":"","SecretHosts":null,"SecretSpecs":null},"instance_id":"iid-1","remove_on_exit":false,"removal_marker":false,"stop_reason":"clean","supervisor_pid":4242,"guest_tap_name":"nxg-aabbccddee"}`

func TestNetMode_LegacyRecordLoadsAsTapAndReserialisesIdentically(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(in, []byte(legacyTapRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := readRecord(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.toDomain().NetMode; got != "" {
		t.Fatalf("legacy record NetMode = %q, want empty (tap)", got)
	}
	if err := writeRecord(out, toRecord(r.toDomain())); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != legacyTapRecord {
		t.Errorf("re-serialised record differs\n got: %s\nwant: %s", got, legacyTapRecord)
	}
}

func TestNetMode_RoundTrip(t *testing.T) {
	sb := domain.Sandbox{ID: domain.SandboxID{9}, Name: "n", Project: "p", NetMode: domain.NetModeVhostUser}
	if got := toRecord(sb).toDomain().NetMode; got != domain.NetModeVhostUser {
		t.Errorf("NetMode = %q, want vhost-user", got)
	}
}
