package cloudhypervisor

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

const vhostNetConfig = `{"cpus":{"boot_vcpus":1},"net":[{"vhost_user":true,"vhost_socket":"/run/n/vhost-A.sock","mac":"52:54:00:aa:bb:cc","num_queues":2}],"memory":{"size":1,"shared":true}}`

func TestSnapshotNetBackend(t *testing.T) {
	tap := `{"net":[{"tap":"nxg-abc","mac":"52:54:00:aa:bb:cc"}]}`
	cases := []struct {
		name, cfg string
		want      netBackend
		wantErr   error
	}{
		{"vhost-user", vhostNetConfig, netBackend{domain.NetModeVhostUser, "/run/n/vhost-A.sock"}, nil},
		{"no net field", `{"cpus":{}}`, netBackend{}, errNoNet},
		{"empty net", `{"net":[]}`, netBackend{}, errNoNet},
	}
	for _, c := range cases {
		got, err := snapshotNetBackend([]byte(c.cfg))
		if !errors.Is(err, c.wantErr) || got != c.want {
			t.Errorf("%s: got %+v, %v; want %+v, %v", c.name, got, err, c.want, c.wantErr)
		}
	}
	if _, err := snapshotNetBackend([]byte(tap)); err == nil || !strings.Contains(err.Error(), "tap networking was removed") {
		t.Errorf("tap snapshot must be refused with the migration message, got %v", err)
	}
	if _, err := snapshotNetBackend([]byte(`{"net":[{"vhost_user":true}]}`)); err == nil {
		t.Error("vhost_user without socket must error")
	}
}

func TestRewriteConfigNetVhostSocket(t *testing.T) {
	out, err := rewriteConfigNetVhostSocket([]byte(vhostNetConfig), "/run/n/vhost-A.sock", "/run/n/vhost-B.sock")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Net    []map[string]any `json:"net"`
		Memory map[string]any   `json:"memory"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	n := got.Net[0]
	if n["vhost_socket"] != "/run/n/vhost-B.sock" || n["vhost_user"] != true || n["mac"] != "52:54:00:aa:bb:cc" || got.Memory["shared"] != true {
		t.Errorf("rewritten config lost or kept wrong fields: %s", out)
	}
	if _, err := rewriteConfigNetVhostSocket([]byte(vhostNetConfig), "/other.sock", "/x"); err == nil {
		t.Error("mismatched old socket must error")
	}
}
