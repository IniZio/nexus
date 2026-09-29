//go:build linux

package vhostnet

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Design rationale and the CH v53 message audit: doc/design/zero-host-deps-vhostnet.md.

const (
	reqGetFeatures         = 1
	reqSetFeatures         = 2
	reqSetOwner            = 3
	reqResetOwner          = 4
	reqSetMemTable         = 5
	reqSetVringNum         = 8
	reqSetVringAddr        = 9
	reqSetVringBase        = 10
	reqGetVringBase        = 11
	reqSetVringKick        = 12
	reqSetVringCall        = 13
	reqSetVringErr         = 14
	reqGetProtocolFeatures = 15
	reqSetProtocolFeatures = 16
	reqSetVringEnable      = 18
	reqGetMaxMemSlots      = 36
	reqAddMemReg           = 37
	reqRemMemReg           = 38
)

const (
	flagVersionMask = 0x3
	flagVersion1    = 0x1
	flagReply       = 0x4
	flagNeedReply   = 0x8

	hdrSize     = 12
	maxBodySize = 4096
	maxRegions  = 32
	regionSize  = 32
	vringNoFD   = 1 << 8
	vringIdxMsk = 0xff

	featNetMAC           uint64 = 1 << 5
	featProtocolFeatures uint64 = 1 << 30
	featVersion1         uint64 = 1 << 32

	pfReplyAck          uint64 = 1 << 3
	pfConfigureMemSlots uint64 = 1 << 15

	offeredFeatures         = featVersion1 | featNetMAC | featProtocolFeatures
	offeredProtocolFeatures = pfReplyAck | pfConfigureMemSlots

	maxMemSlots = 509
	numQueues   = 2
	maxRegion   = 1 << 44
)

var errProto = errors.New("vhostnet: protocol violation")

type header struct {
	Request uint32
	Flags   uint32
	Size    uint32
}

func (h header) needReply() bool { return h.Flags&flagNeedReply != 0 }

func parseHeader(b []byte) (header, error) {
	if len(b) < hdrSize {
		return header{}, fmt.Errorf("%w: short header", errProto)
	}
	h := header{
		Request: binary.LittleEndian.Uint32(b[0:]),
		Flags:   binary.LittleEndian.Uint32(b[4:]),
		Size:    binary.LittleEndian.Uint32(b[8:]),
	}
	if h.Flags&flagVersionMask != flagVersion1 {
		return h, fmt.Errorf("%w: bad version flags %#x", errProto, h.Flags)
	}
	if h.Flags&flagReply != 0 {
		return h, fmt.Errorf("%w: reply flag from master", errProto)
	}
	if h.Size > maxBodySize {
		return h, fmt.Errorf("%w: body size %d", errProto, h.Size)
	}
	return h, nil
}

func appendHeader(dst []byte, h header) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, h.Request)
	dst = binary.LittleEndian.AppendUint32(dst, h.Flags)
	return binary.LittleEndian.AppendUint32(dst, h.Size)
}

type regionDesc struct {
	GPA    uint64
	Size   uint64
	UAddr  uint64
	Offset uint64
}

type vringAddr struct {
	Index uint32
	Flags uint32
	Desc  uint64
	Used  uint64
	Avail uint64
	Log   uint64
}

type message struct {
	Req     uint32
	NeedRpl bool
	U64     uint64
	Index   uint32
	Num     uint32
	NoFD    bool
	Addr    vringAddr
	Regions []regionDesc
	NumFDs  int
}

func decodeRegion(b []byte) (regionDesc, error) {
	r := regionDesc{
		GPA:    binary.LittleEndian.Uint64(b[0:]),
		Size:   binary.LittleEndian.Uint64(b[8:]),
		UAddr:  binary.LittleEndian.Uint64(b[16:]),
		Offset: binary.LittleEndian.Uint64(b[24:]),
	}
	if r.Size == 0 || r.Size > maxRegion {
		return r, fmt.Errorf("%w: region size %#x", errProto, r.Size)
	}
	if r.GPA+r.Size < r.GPA || r.UAddr+r.Size < r.UAddr || r.Offset+r.Size < r.Offset {
		return r, fmt.Errorf("%w: region overflows", errProto)
	}
	return r, nil
}

func expectBody(name string, body []byte, want int) error {
	if len(body) != want {
		return fmt.Errorf("%w: %s body %d, want %d", errProto, name, len(body), want)
	}
	return nil
}

// decodeMessage validates one request body and the count of received fds.
// It is pure so it can be fuzzed without a socket.
func decodeMessage(h header, body []byte, nfds int) (message, error) {
	m := message{Req: h.Request, NeedRpl: h.needReply(), NumFDs: nfds}
	if int(h.Size) != len(body) {
		return m, fmt.Errorf("%w: size %d != body %d", errProto, h.Size, len(body))
	}
	wantFDs := 0
	switch h.Request {
	case reqGetFeatures, reqSetOwner, reqResetOwner, reqGetProtocolFeatures, reqGetMaxMemSlots:
		if err := expectBody("empty", body, 0); err != nil {
			return m, err
		}
	case reqSetFeatures, reqSetProtocolFeatures:
		if err := expectBody("u64", body, 8); err != nil {
			return m, err
		}
		m.U64 = binary.LittleEndian.Uint64(body)
	case reqSetVringNum, reqSetVringBase, reqGetVringBase, reqSetVringEnable:
		if err := expectBody("vring state", body, 8); err != nil {
			return m, err
		}
		m.Index = binary.LittleEndian.Uint32(body[0:])
		m.Num = binary.LittleEndian.Uint32(body[4:])
		if m.Index >= numQueues {
			return m, fmt.Errorf("%w: vring index %d", errProto, m.Index)
		}
	case reqSetVringKick, reqSetVringCall, reqSetVringErr:
		if err := expectBody("vring fd", body, 8); err != nil {
			return m, err
		}
		m.U64 = binary.LittleEndian.Uint64(body)
		m.Index = uint32(m.U64 & vringIdxMsk)
		m.NoFD = m.U64&vringNoFD != 0
		if m.U64&^uint64(vringIdxMsk|vringNoFD) != 0 || m.Index >= numQueues {
			return m, fmt.Errorf("%w: vring fd word %#x", errProto, m.U64)
		}
		if !m.NoFD {
			wantFDs = 1
		}
	case reqSetVringAddr:
		if err := expectBody("vring addr", body, 40); err != nil {
			return m, err
		}
		m.Addr = vringAddr{
			Index: binary.LittleEndian.Uint32(body[0:]),
			Flags: binary.LittleEndian.Uint32(body[4:]),
			Desc:  binary.LittleEndian.Uint64(body[8:]),
			Used:  binary.LittleEndian.Uint64(body[16:]),
			Avail: binary.LittleEndian.Uint64(body[24:]),
			Log:   binary.LittleEndian.Uint64(body[32:]),
		}
		m.Index = m.Addr.Index
		if m.Index >= numQueues {
			return m, fmt.Errorf("%w: vring index %d", errProto, m.Index)
		}
	case reqSetMemTable:
		if len(body) < 8 {
			return m, fmt.Errorf("%w: short mem table", errProto)
		}
		n := int(binary.LittleEndian.Uint32(body))
		if n == 0 || n > maxRegions || len(body) != 8+n*regionSize {
			return m, fmt.Errorf("%w: mem table of %d regions in %d bytes", errProto, n, len(body))
		}
		for i := range n {
			r, err := decodeRegion(body[8+i*regionSize:])
			if err != nil {
				return m, err
			}
			m.Regions = append(m.Regions, r)
		}
		wantFDs = n
	case reqAddMemReg, reqRemMemReg:
		if err := expectBody("single region", body, 8+regionSize); err != nil {
			return m, err
		}
		r, err := decodeRegion(body[8:])
		if err != nil {
			return m, err
		}
		m.Regions = []regionDesc{r}
		if h.Request == reqAddMemReg {
			wantFDs = 1
		}
	default:
		return m, fmt.Errorf("%w: unsupported request %d", errProto, h.Request)
	}
	if nfds != wantFDs {
		return m, fmt.Errorf("%w: request %d carried %d fds, want %d", errProto, h.Request, nfds, wantFDs)
	}
	return m, nil
}
