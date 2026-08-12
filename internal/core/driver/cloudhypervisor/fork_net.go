package cloudhypervisor

import (
	"encoding/json"
	"errors"
	"fmt"
)

// errNoNet is returned by findNetTap when config.json has no "net" field or
// the net array is empty. This is expected for vsock-only VMs; callers should
// skip net isolation in that case, exactly as errNoDisks skips disk isolation
// for initramfs-only VMs.
var errNoNet = errors.New("no net devices configured in config.json")

// findNetTap parses a CH config.json blob and returns the "tap" name of the
// first net device entry.
//
// Returns errNoNet when config.json has no "net" field or the array is empty —
// the caller should skip net isolation for vsock-only snapshots.
func findNetTap(configJSON []byte) (string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return "", fmt.Errorf("unmarshal config.json: %w", err)
	}
	netRaw, ok := top["net"]
	if !ok {
		return "", errNoNet
	}
	var nets []map[string]json.RawMessage
	if err := json.Unmarshal(netRaw, &nets); err != nil {
		return "", fmt.Errorf("unmarshal net array: %w", err)
	}
	if len(nets) == 0 {
		return "", errNoNet
	}
	tapRaw, ok := nets[0]["tap"]
	if !ok {
		return "", fmt.Errorf("net[0] has no \"tap\" field")
	}
	var tap string
	if err := json.Unmarshal(tapRaw, &tap); err != nil {
		return "", fmt.Errorf("decode net[0].tap: %w", err)
	}
	return tap, nil
}

// rewriteConfigNetTap returns a rewritten copy of configJSON in which the net
// entry whose "tap" == oldTap has its "tap" replaced by newTap. All other
// top-level fields and all other per-net-entry fields are preserved verbatim
// via a map[string]json.RawMessage round-trip — unknown fields are never
// dropped.
//
// If multiple net entries are present, only the first one matching oldTap is
// rewritten; others are left unchanged. This mirrors the single-entry rewrite
// used by rewriteConfigDiskPath.
//
// Returns an error if no net entry with tap == oldTap is found.
func rewriteConfigNetTap(configJSON []byte, oldTap, newTap string) ([]byte, error) {
	// Preserve the top-level config byte-for-byte except for "net".
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return nil, fmt.Errorf("unmarshal config.json: %w", err)
	}
	netRaw, ok := top["net"]
	if !ok {
		return nil, fmt.Errorf("config.json has no \"net\" field")
	}

	// Preserve each net entry byte-for-byte except for the matched "tap".
	var nets []map[string]json.RawMessage
	if err := json.Unmarshal(netRaw, &nets); err != nil {
		return nil, fmt.Errorf("unmarshal net array: %w", err)
	}

	rewritten := false
	for i, net := range nets {
		tapRaw, ok := net["tap"]
		if !ok {
			continue
		}
		var tap string
		if err := json.Unmarshal(tapRaw, &tap); err != nil {
			continue
		}
		if tap == oldTap {
			newTapRaw, merr := json.Marshal(newTap)
			if merr != nil {
				return nil, fmt.Errorf("marshal new tap name: %w", merr)
			}
			nets[i]["tap"] = newTapRaw
			rewritten = true
			break // rewrite only the first match; leave other net entries unchanged
		}
	}
	if !rewritten {
		return nil, fmt.Errorf("config.json: no net entry with tap %q", oldTap)
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
// field. This is unexpected for nexus3 VMs (all have a vsock device) but
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
