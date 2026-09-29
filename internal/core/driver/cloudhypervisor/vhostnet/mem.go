//go:build linux

package vhostnet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

var errBadAddr = errors.New("vhostnet: address outside guest memory")

type region struct {
	desc regionDesc
	m    []byte
	data []byte
}

type memTable struct {
	regions []*region
}

func mapRegion(d regionDesc, fd int) (*region, error) {
	page := uint64(unix.Getpagesize())
	pageOff := d.Offset % page
	length := pageOff + d.Size
	if length < d.Size || length > uint64(1<<62) {
		return nil, fmt.Errorf("%w: region length", errProto)
	}
	m, err := unix.Mmap(fd, int64(d.Offset-pageOff), int(length), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("vhostnet: mmap region: %w", err)
	}
	return &region{desc: d, m: m, data: m[pageOff:]}, nil
}

func (r *region) unmap() { _ = unix.Munmap(r.m) }

func (t *memTable) clone() *memTable {
	return &memTable{regions: append([]*region(nil), t.regions...)}
}

func (t *memTable) unmapAll() {
	for _, r := range t.regions {
		r.unmap()
	}
	t.regions = nil
}

// spans appends the host slices backing guest-physical [gpa, gpa+n), which may
// cross adjacent regions.
func (t *memTable) spans(dst [][]byte, gpa, n uint64) ([][]byte, error) {
	if gpa+n < gpa {
		return dst, errBadAddr
	}
	for n > 0 {
		var hit *region
		for _, r := range t.regions {
			if gpa >= r.desc.GPA && gpa-r.desc.GPA < r.desc.Size {
				hit = r
				break
			}
		}
		if hit == nil {
			return dst, errBadAddr
		}
		off := gpa - hit.desc.GPA
		take := min(n, hit.desc.Size-off)
		dst = append(dst, hit.data[off:off+take:off+take])
		gpa += take
		n -= take
	}
	return dst, nil
}

// userSlice resolves a master-virtual address range that must sit inside one
// region; vring structures are addressed this way.
func (t *memTable) userSlice(uaddr, n uint64) ([]byte, error) {
	if uaddr+n < uaddr {
		return nil, errBadAddr
	}
	for _, r := range t.regions {
		if uaddr >= r.desc.UAddr && uaddr-r.desc.UAddr < r.desc.Size {
			off := uaddr - r.desc.UAddr
			if n > r.desc.Size-off {
				return nil, errBadAddr
			}
			return r.data[off : off+n : off+n], nil
		}
	}
	return nil, errBadAddr
}

func hostIsLittleEndian() bool { return binary.NativeEndian.Uint16([]byte{1, 0}) == 1 }

// The Go sync/atomic package has no 16-bit operations, so ring indices are
// accessed through their aligned enclosing 32-bit word (little-endian host).
func wordOf(b []byte, off int) (*atomic.Uint32, uint) {
	p := unsafe.Pointer(&b[off])
	skew := int(uintptr(p) & 3)
	return (*atomic.Uint32)(unsafe.Add(p, -skew)), uint(skew) * 8
}

func load16(b []byte, off int) uint16 {
	w, shift := wordOf(b, off)
	return uint16(w.Load() >> shift)
}

func store16(b []byte, off int, v uint16) {
	w, shift := wordOf(b, off)
	for {
		old := w.Load()
		nw := old&^(0xffff<<shift) | uint32(v)<<shift
		if w.CompareAndSwap(old, nw) {
			return
		}
	}
}
