package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const legacyTapRecord = `{"schema_version":1,"id":"sb-04106000000000000000000000","name":"tapbox","project":"proj","state":"stopped","envelope":{"ImageDigest":"","AllowedHosts":null,"SSHPublicKey":"","SecretHosts":null,"SecretSpecs":null},"instance_id":"iid-1","remove_on_exit":false,"removal_marker":false,"stop_reason":"clean","supervisor_pid":4242,"guest_tap_name":"nxt-aabbccddee","net_mode":"tap"}`

func TestLegacyTapRecordLoadsAndRewritesClean(t *testing.T) {
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
	if err := writeRecord(out, toRecord(r.toDomain())); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "guest_tap_name") || strings.Contains(string(got), "net_mode") {
		t.Errorf("re-serialised record still carries guest_tap_name or net_mode: %s", got)
	}
}
