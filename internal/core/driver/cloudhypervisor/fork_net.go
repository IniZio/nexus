package cloudhypervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus/internal/core/artifact"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

// errNoNet is returned by snapshotNetBackend when config.json has no "net"
// field or the net array is empty. This is expected for vsock-only VMs;
// callers should skip net isolation in that case, exactly as errNoDisks skips
// disk isolation for initramfs-only VMs.
var errNoNet = errors.New("no net devices configured in config.json")

type netBackend struct {
	mode domain.NetMode
	ref  string // vhost-user socket path
}

// snapshotNetBackend derives the mode from net[0]'s shape; errNoNet if netless.
func snapshotNetBackend(configJSON []byte) (netBackend, error) {
	var top struct {
		Net []struct {
			VhostUser bool   `json:"vhost_user"`
			Socket    string `json:"vhost_socket"`
		} `json:"net"`
	}
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return netBackend{}, fmt.Errorf("unmarshal config.json: %w", err)
	}
	if len(top.Net) == 0 {
		return netBackend{}, errNoNet
	}
	n := top.Net[0]
	if n.VhostUser {
		if n.Socket == "" {
			return netBackend{}, fmt.Errorf("net[0] is vhost_user but has no \"vhost_socket\"")
		}
		return netBackend{mode: domain.NetModeVhostUser, ref: n.Socket}, nil
	}
	return netBackend{}, fmt.Errorf("net[0] is not vhost_user: %w", driver.ErrTapSnapshot)
}

type netRewrite struct {
	parent, child string
}

func (r netRewrite) apply(configJSON []byte) ([]byte, error) {
	return rewriteConfigNetVhostSocket(configJSON, r.parent, r.child)
}

// SnapshotNetMode reports a snapshot's NIC mode (zero when netless).
func (d *CHDriver) SnapshotNetMode(snap artifact.Snapshot) (domain.NetMode, error) {
	cfg, err := os.ReadFile(filepath.Join(d.snapshotDirPath(snap.ID), "config.json"))
	if err != nil {
		return "", fmt.Errorf("cloudhypervisor: snapshot net mode: %w", err)
	}
	nb, err := snapshotNetBackend(cfg)
	if errors.Is(err, errNoNet) {
		return "", nil
	}
	return nb.mode, err
}

var _ driver.SnapshotNetModer = (*CHDriver)(nil)

func rewriteConfigNetVhostSocket(configJSON []byte, oldSocket, newSocket string) ([]byte, error) {
	return rewriteConfigNetField(configJSON, "vhost_socket", oldSocket, newSocket)
}

func rewriteConfigNetField(configJSON []byte, field, oldVal, newVal string) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return nil, fmt.Errorf("unmarshal config.json: %w", err)
	}
	netRaw, ok := top["net"]
	if !ok {
		return nil, fmt.Errorf("config.json has no \"net\" field")
	}
	var nets []map[string]json.RawMessage
	if err := json.Unmarshal(netRaw, &nets); err != nil {
		return nil, fmt.Errorf("unmarshal net array: %w", err)
	}

	rewritten := false
	for i, net := range nets {
		raw, ok := net[field]
		if !ok {
			continue
		}
		var cur string
		if err := json.Unmarshal(raw, &cur); err != nil {
			continue
		}
		if cur == oldVal {
			newRaw, merr := json.Marshal(newVal)
			if merr != nil {
				return nil, fmt.Errorf("marshal new %s: %w", field, merr)
			}
			nets[i][field] = newRaw
			rewritten = true
			break
		}
	}
	if !rewritten {
		return nil, fmt.Errorf("config.json: no net entry with %s %q", field, oldVal)
	}

	newNetsRaw, err := json.Marshal(nets)
	if err != nil {
		return nil, fmt.Errorf("re-encode nets: %w", err)
	}
	top["net"] = newNetsRaw

	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("re-encode config.json: %w", err)
	}
	return out, nil
}

// errNoVsock is returned by findVsockPath when config.json has no "vsock"
// field. This is unexpected for nexus VMs (all have a vsock device) but
// allows callers to skip vsock path rewriting gracefully.
var errNoVsock = errors.New("no vsock device configured in config.json")

// findVsockPath parses a CH config.json blob and returns the "socket" path of
// the vsock device (the AF_UNIX socket CH's vsock multiplexer binds).
//
// Returns errNoVsock when config.json has no "vsock" field.
func findVsockPath(configJSON []byte) (string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return "", fmt.Errorf("unmarshal config.json: %w", err)
	}
	vsockRaw, ok := top["vsock"]
	if !ok {
		return "", errNoVsock
	}
	var vsock map[string]json.RawMessage
	if err := json.Unmarshal(vsockRaw, &vsock); err != nil {
		return "", fmt.Errorf("unmarshal vsock: %w", err)
	}
	socketRaw, ok := vsock["socket"]
	if !ok {
		return "", fmt.Errorf("vsock has no \"socket\" field")
	}
	var socket string
	if err := json.Unmarshal(socketRaw, &socket); err != nil {
		return "", fmt.Errorf("decode vsock.socket: %w", err)
	}
	return socket, nil
}

// rewriteConfigVsockPath returns a rewritten copy of configJSON in which the
// vsock device's "socket" path is replaced by newSocket. All other top-level
// fields and all other vsock fields are preserved verbatim via a
// map[string]json.RawMessage round-trip — unknown fields are never dropped.
//
// Returns an error if the vsock field is absent or its socket path does not
// match oldSocket (guards against accidentally rewriting the wrong snapshot).
func rewriteConfigVsockPath(configJSON []byte, oldSocket, newSocket string) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return nil, fmt.Errorf("unmarshal config.json: %w", err)
	}
	vsockRaw, ok := top["vsock"]
	if !ok {
		return nil, fmt.Errorf("config.json has no \"vsock\" field")
	}
	var vsock map[string]json.RawMessage
	if err := json.Unmarshal(vsockRaw, &vsock); err != nil {
		return nil, fmt.Errorf("unmarshal vsock: %w", err)
	}
	socketRaw, ok := vsock["socket"]
	if !ok {
		return nil, fmt.Errorf("vsock has no \"socket\" field")
	}
	var socket string
	if err := json.Unmarshal(socketRaw, &socket); err != nil {
		return nil, fmt.Errorf("decode vsock.socket: %w", err)
	}
	if socket != oldSocket {
		return nil, fmt.Errorf("config.json: vsock.socket %q != expected %q", socket, oldSocket)
	}
	newSocketRaw, err := json.Marshal(newSocket)
	if err != nil {
		return nil, fmt.Errorf("marshal new vsock socket path: %w", err)
	}
	vsock["socket"] = newSocketRaw
	newVsockRaw, err := json.Marshal(vsock)
	if err != nil {
		return nil, fmt.Errorf("re-encode vsock: %w", err)
	}
	top["vsock"] = newVsockRaw
	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("re-encode config.json: %w", err)
	}
	return out, nil
}
