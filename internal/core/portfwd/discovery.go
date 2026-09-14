package portfwd

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	GuestPortBase = uint16(1024)
	GuestPortTop  = uint16(11023)
)

type SandboxStatus string

const (
	SandboxStatusRunning SandboxStatus = "running"
	SandboxStatusStopped SandboxStatus = "stopped"
	SandboxStatusCreated SandboxStatus = "created"
	SandboxStatusPaused  SandboxStatus = "paused"
)

type SandboxRef struct {
	ID     string
	Name   string
	Status SandboxStatus
}

// Backend abstracts the nexus3 exec path for /proc/net reads. T4b wires this.
type Backend interface {
	ListSandboxes(ctx context.Context) ([]SandboxRef, error)
	ReadProcNet(ctx context.Context, sandboxID string) (tcp, tcp6 []byte, err error)
}

type PortBind struct {
	Port     uint16
	BindAddr string
}

type Listener struct {
	Port     uint16
	BindAddr string
	Sandbox  SandboxRef
}

type Discoverer struct {
	Backend Backend
}

func ParseNetstat(text string) []PortBind {
	var out []PortBind
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[5] != "LISTEN" {
			continue
		}
		addr := fields[3]
		i := strings.LastIndex(addr, ":")
		if i < 0 {
			continue
		}
		p, err := strconv.ParseUint(addr[i+1:], 10, 16)
		if err != nil {
			continue
		}
		out = append(out, PortBind{Port: uint16(p), BindAddr: addr[:i]})
	}
	return out
}

func parseProcNetFile(text string) []PortBind {
	var out []PortBind
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] == "sl" {
			continue
		}
		if len(fields[3]) != 2 {
			continue
		}
		st, err := strconv.ParseUint(fields[3], 16, 8)
		if err != nil || st != 0x0A {
			continue
		}
		i := strings.LastIndex(fields[1], ":")
		if i < 0 {
			continue
		}
		addrHex, portHex := fields[1][:i], fields[1][i+1:]
		p, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		var ipStr string
		switch len(addrHex) {
		case 8:
			v, e := strconv.ParseUint(addrHex, 16, 32)
			if e != nil {
				continue
			}
			b := [4]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
			ipStr = net.IP(b[:]).String()
		case 32:
			b := [16]byte{}
			ok := true
			for j := 0; j < 4; j++ {
				w, e := strconv.ParseUint(addrHex[j*8:(j+1)*8], 16, 32)
				if e != nil {
					ok = false
					break
				}
				b[j*4] = byte(w)
				b[j*4+1] = byte(w >> 8)
				b[j*4+2] = byte(w >> 16)
				b[j*4+3] = byte(w >> 24)
			}
			if !ok {
				continue
			}
			ipStr = net.IP(b[:]).String()
		default:
			continue
		}
		out = append(out, PortBind{Port: uint16(p), BindAddr: ipStr})
	}
	return out
}

func ParseProcNetTCP(text string) []PortBind  { return parseProcNetFile(text) }
func ParseProcNetTCP6(text string) []PortBind { return parseProcNetFile(text) }

type FilterResult struct {
	Forwardable []Listener
	OutOfRange  []Listener
	Reserved    []Listener
}

func FilterListeners(ls []Listener, exclude []uint16) FilterResult {
	excSet := make(map[uint16]bool, len(exclude))
	for _, p := range exclude {
		excSet[p] = true
	}
	var r FilterResult
	for _, l := range ls {
		if l.Port < GuestPortBase || excSet[l.Port] {
			r.Reserved = append(r.Reserved, l)
			continue
		}
		if l.Port > GuestPortTop {
			r.OutOfRange = append(r.OutOfRange, l)
			continue
		}
		r.Forwardable = append(r.Forwardable, l)
	}
	return r
}

func (d *Discoverer) DiscoverOne(ctx context.Context, ref SandboxRef) ([]Listener, error) {
	tcp, tcp6, err := d.Backend.ReadProcNet(ctx, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s (%s): exec: %w", ref.ID, ref.Name, err)
	}
	binds := append(ParseProcNetTCP(string(tcp)), ParseProcNetTCP6(string(tcp6))...)
	out := make([]Listener, len(binds))
	for i, b := range binds {
		out[i] = Listener{Port: b.Port, BindAddr: b.BindAddr, Sandbox: ref}
	}
	return out, nil
}

func (d *Discoverer) DiscoverAll(ctx context.Context) ([]Listener, error) {
	refs, err := d.Backend.ListSandboxes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	var all []Listener
	for _, ref := range refs {
		if ref.Status != SandboxStatusRunning {
			continue
		}
		ls, err := d.DiscoverOne(ctx, ref)
		if err != nil {
			return nil, err
		}
		all = append(all, ls...)
	}
	return all, nil
}
