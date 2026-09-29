//go:build linux

package vhostnet

import (
	"encoding/binary"
	"testing"
)

func validMsg(req uint32, body []byte) []byte {
	b := appendHeader(nil, header{Request: req, Flags: flagVersion1, Size: uint32(len(body))})
	return append(b, body...)
}

func TestDecodeAcceptsCHRequests(t *testing.T) {
	region := regionBody(0x1000, 0x2000, 0x7f0000000000, 0)
	memTable := append(binary.LittleEndian.AppendUint64(nil, 1), region...)
	single := append(binary.LittleEndian.AppendUint64(nil, 0), region...)
	vaddr := make([]byte, 40)
	cases := []struct {
		name string
		req  uint32
		body []byte
		fds  int
	}{
		{"get features", reqGetFeatures, nil, 0},
		{"set features", reqSetFeatures, u64(offeredFeatures), 0},
		{"set owner", reqSetOwner, nil, 0},
		{"reset owner", reqResetOwner, nil, 0},
		{"get pfeatures", reqGetProtocolFeatures, nil, 0},
		{"set pfeatures", reqSetProtocolFeatures, u64(offeredProtocolFeatures), 0},
		{"mem table", reqSetMemTable, memTable, 1},
		{"add reg", reqAddMemReg, single, 1},
		{"rem reg", reqRemMemReg, single, 0},
		{"max slots", reqGetMaxMemSlots, nil, 0},
		{"vring num", reqSetVringNum, u64(256 << 32), 0},
		{"vring addr", reqSetVringAddr, vaddr, 0},
		{"vring base", reqSetVringBase, u64(0), 0},
		{"get vring base", reqGetVringBase, u64(1), 0},
		{"vring kick", reqSetVringKick, u64(1), 1},
		{"vring call", reqSetVringCall, u64(0), 1},
		{"vring call nofd", reqSetVringCall, u64(vringNoFD), 0},
		{"vring enable", reqSetVringEnable, u64(1<<32 | 1), 0},
	}
	for _, c := range cases {
		msg := validMsg(c.req, c.body)
		h, err := parseHeader(msg)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, err := decodeMessage(h, msg[hdrSize:], c.fds); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	region := regionBody(0x1000, 0x2000, 0x7f0000000000, 0)
	single := append(binary.LittleEndian.AppendUint64(nil, 0), region...)
	cases := []struct {
		name string
		req  uint32
		body []byte
		fds  int
	}{
		{"unknown request", 9999, nil, 0},
		{"unoffered request", 34, nil, 0},
		{"short u64", reqSetFeatures, []byte{1, 2, 3}, 0},
		{"queue index out of range", reqSetVringNum, u64(5), 0},
		{"kick without fd", reqSetVringKick, u64(0), 0},
		{"kick with stray bits", reqSetVringKick, u64(1 << 20), 1},
		{"stray fd", reqSetOwner, nil, 1},
		{"mem table zero regions", reqSetMemTable, make([]byte, 8), 0},
		{"mem table count mismatch", reqSetMemTable, append(binary.LittleEndian.AppendUint64(nil, 3), region...), 3},
		{"mem table fd mismatch", reqSetMemTable, append(binary.LittleEndian.AppendUint64(nil, 1), region...), 2},
		{"region overflow", reqAddMemReg, append(binary.LittleEndian.AppendUint64(nil, 0), regionBody(^uint64(0)-4, 0x100, 0, 0)...), 1},
		{"region zero size", reqAddMemReg, append(binary.LittleEndian.AppendUint64(nil, 0), regionBody(0, 0, 0, 0)...), 1},
		{"add reg no fd", reqAddMemReg, single, 0},
	}
	for _, c := range cases {
		msg := validMsg(c.req, c.body)
		h, err := parseHeader(msg)
		if err != nil {
			t.Fatalf("%s: header: %v", c.name, err)
		}
		if _, err := decodeMessage(h, msg[hdrSize:], c.fds); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	for name, hb := range map[string][]byte{
		"bad version":   appendHeader(nil, header{Request: 1, Flags: 0, Size: 0}),
		"reply flag":    appendHeader(nil, header{Request: 1, Flags: flagVersion1 | flagReply}),
		"oversize body": appendHeader(nil, header{Request: 1, Flags: flagVersion1, Size: maxBodySize + 1}),
		"short":         {1, 2, 3},
	} {
		if _, err := parseHeader(hb); err == nil {
			t.Errorf("header %s accepted", name)
		}
	}
}

func FuzzDecodeMessage(f *testing.F) {
	region := regionBody(0x1000, 0x2000, 0x7f0000000000, 0)
	f.Add(validMsg(reqSetMemTable, append(binary.LittleEndian.AppendUint64(nil, 1), region...)), 1)
	f.Add(validMsg(reqSetVringAddr, make([]byte, 40)), 0)
	f.Add(validMsg(reqSetVringKick, u64(1)), 1)
	f.Add(validMsg(reqAddMemReg, append(binary.LittleEndian.AppendUint64(nil, 0), region...)), 1)
	f.Add(validMsg(reqSetFeatures, u64(offeredFeatures)), 0)
	f.Fuzz(func(t *testing.T, data []byte, nfds int) {
		h, err := parseHeader(data)
		if err != nil {
			return
		}
		body := data[hdrSize:]
		if len(body) > int(h.Size) {
			body = body[:h.Size]
		}
		m, err := decodeMessage(h, body, nfds)
		if err != nil {
			return
		}
		if len(m.Regions) > maxRegions {
			t.Fatalf("decoded %d regions", len(m.Regions))
		}
		for _, r := range m.Regions {
			if r.Size == 0 || r.GPA+r.Size < r.GPA || r.UAddr+r.Size < r.UAddr || r.Offset+r.Size < r.Offset {
				t.Fatalf("accepted bad region %+v", r)
			}
		}
		if m.Index >= numQueues && m.Req != reqSetMemTable && m.Req != reqAddMemReg && m.Req != reqRemMemReg {
			switch m.Req {
			case reqSetVringNum, reqSetVringAddr, reqSetVringBase, reqGetVringBase, reqSetVringKick, reqSetVringCall, reqSetVringErr, reqSetVringEnable:
				t.Fatalf("accepted queue index %d for request %d", m.Index, m.Req)
			}
		}
	})
}
