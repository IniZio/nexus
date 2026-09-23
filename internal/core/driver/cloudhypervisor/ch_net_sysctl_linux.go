//go:build linux

package cloudhypervisor

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const nlaFNested = 1 << 15
const afInetInIFLA = unix.AF_INET
const ipv4DevconfForwarding = 1 // 1-based NLA type (linux/ip.h enum ipv4_devconf index 0 → type 1)

// buildSetForwardingMsg builds a 52-byte RTM_SETLINK message setting
// IPV4_DEVCONF_FORWARDING=value on ifindex via IFLA_AF_SPEC→AF_INET→IFLA_INET_CONF.
// Exported for hermetic byte-encoding tests.
func buildSetForwardingMsg(ifindex int, value uint32) []byte {
	ne := binary.NativeEndian
	const hdr = 4
	leafAttr := hdr + 4
	inetConf := hdr + leafAttr
	afInet := hdr + inetConf
	afSpec := hdr + afInet
	const nlmHdr = 16
	const ifInfoHdr = 16
	total := nlmHdr + ifInfoHdr + afSpec

	buf := make([]byte, total)
	off := 0

	ne.PutUint32(buf[off:], uint32(total))
	ne.PutUint16(buf[off+4:], unix.RTM_SETLINK)
	ne.PutUint16(buf[off+6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	ne.PutUint32(buf[off+8:], 1)
	ne.PutUint32(buf[off+12:], 0)
	off += nlmHdr

	buf[off] = 0
	buf[off+1] = 0
	ne.PutUint16(buf[off+2:], 0)
	ne.PutUint32(buf[off+4:], uint32(ifindex))
	ne.PutUint32(buf[off+8:], 0)
	ne.PutUint32(buf[off+12:], 0)
	off += ifInfoHdr

	ne.PutUint16(buf[off:], uint16(afSpec))
	ne.PutUint16(buf[off+2:], unix.IFLA_AF_SPEC|nlaFNested)
	off += hdr

	ne.PutUint16(buf[off:], uint16(afInet))
	ne.PutUint16(buf[off+2:], afInetInIFLA|nlaFNested)
	off += hdr

	ne.PutUint16(buf[off:], uint16(inetConf))
	ne.PutUint16(buf[off+2:], unix.IFLA_INET_CONF|nlaFNested)
	off += hdr

	ne.PutUint16(buf[off:], uint16(leafAttr))
	ne.PutUint16(buf[off+2:], ipv4DevconfForwarding)
	ne.PutUint32(buf[off+4:], value)

	return buf
}

func setIfaceForwardingNetlink(iface string) error {
	ni, err := net.InterfaceByName(iface)
	if err != nil {
		return fmt.Errorf("InterfaceByName(%s): %w", iface, err)
	}
	ifindex := ni.Index
	msg := buildSetForwardingMsg(ifindex, 0)
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("socket(AF_NETLINK): %w", err)
	}
	defer unix.Close(fd)
	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	if err := unix.Bind(fd, sa); err != nil {
		return fmt.Errorf("bind(AF_NETLINK): %w", err)
	}
	if err := unix.Sendmsg(fd, msg, nil, sa, 0); err != nil {
		return fmt.Errorf("sendmsg RTM_SETLINK: %w", err)
	}
	return recvNetlinkAck(fd)
}

func recvNetlinkAck(fd int) error {
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return fmt.Errorf("recvfrom netlink ACK: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(buf[:n])
	if err != nil {
		return fmt.Errorf("ParseNetlinkMessage: %w", err)
	}
	for _, m := range msgs {
		if m.Header.Type == syscall.NLMSG_ERROR {
			if len(m.Data) < 4 {
				return fmt.Errorf("NLMSG_ERROR: short payload (%d bytes)", len(m.Data))
			}
			code := int32(binary.NativeEndian.Uint32(m.Data[:4]))
			if code != 0 {
				return fmt.Errorf("RTM_SETLINK: %w", syscall.Errno(-code))
			}
			return nil
		}
	}
	return fmt.Errorf("no NLMSG_ERROR ACK (%d messages)", len(msgs))
}

func provenForwardingZero(iface string) error {
	path := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/forwarding", iface)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(raw)) != "0" {
		return fmt.Errorf("forwarding not zero after netlink set (got %q)", strings.TrimSpace(string(raw)))
	}
	return nil
}
