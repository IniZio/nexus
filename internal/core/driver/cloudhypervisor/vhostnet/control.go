//go:build linux

package vhostnet

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func (d *Device) handle(h header, m message, fds []int) error {
	claimed := 0
	defer func() { closeFDs(fds[claimed:]) }()

	switch m.Req {
	case reqGetFeatures:
		return d.reply(h, offeredFeatures)
	case reqGetProtocolFeatures:
		return d.reply(h, offeredProtocolFeatures)
	case reqGetMaxMemSlots:
		if d.protoFeats&pfConfigureMemSlots == 0 {
			return fmt.Errorf("%w: GET_MAX_MEM_SLOTS without CONFIGURE_MEM_SLOTS", errProto)
		}
		return d.reply(h, maxMemSlots)
	case reqSetFeatures:
		if m.U64&^offeredFeatures != 0 {
			return fmt.Errorf("%w: unoffered features %#x", errProto, m.U64&^offeredFeatures)
		}
		d.features = m.U64
	case reqSetProtocolFeatures:
		if m.U64&^offeredProtocolFeatures != 0 {
			return fmt.Errorf("%w: unoffered protocol features %#x", errProto, m.U64&^offeredProtocolFeatures)
		}
		d.protoFeats = m.U64
	case reqSetOwner:
	case reqResetOwner:
		d.reset()
	case reqSetMemTable:
		d.stopAll()
		table := &memTable{}
		for i, r := range m.Regions {
			reg, err := mapRegion(r, fds[i])
			if err != nil {
				table.unmapAll()
				return err
			}
			table.regions = append(table.regions, reg)
		}
		d.swapMem(table)
		if err := d.startAll(); err != nil {
			return err
		}
	case reqAddMemReg:
		if d.protoFeats&pfConfigureMemSlots == 0 {
			return fmt.Errorf("%w: ADD_MEM_REG without CONFIGURE_MEM_SLOTS", errProto)
		}
		reg, err := mapRegion(m.Regions[0], fds[0])
		if err != nil {
			return err
		}
		d.stopAll()
		table := d.mem.clone()
		table.regions = append(table.regions, reg)
		d.swapMem(table)
		if err := d.startAll(); err != nil {
			return err
		}
	case reqRemMemReg:
		if d.protoFeats&pfConfigureMemSlots == 0 {
			return fmt.Errorf("%w: REM_MEM_REG without CONFIGURE_MEM_SLOTS", errProto)
		}
		d.stopAll()
		if err := d.removeRegion(m.Regions[0]); err != nil {
			_ = d.startAll()
			return err
		}
		if err := d.startAll(); err != nil {
			return err
		}
	case reqSetVringNum:
		if m.Num == 0 || m.Num > maxQueueSize || m.Num&(m.Num-1) != 0 {
			return fmt.Errorf("vhostnet: queue size %d not a power of two in [1,%d]", m.Num, maxQueueSize)
		}
		q := d.q[m.Index]
		q.stopRing()
		q.mu.Lock()
		q.size, q.haveNum = uint16(m.Num), true
		q.mu.Unlock()
	case reqSetVringAddr:
		if m.Addr.Flags != 0 {
			return fmt.Errorf("vhostnet: vring addr flags %#x (logging) unsupported", m.Addr.Flags)
		}
		q := d.q[m.Index]
		q.stopRing()
		q.mu.Lock()
		q.descU, q.usedU, q.availU, q.haveAdr = m.Addr.Desc, m.Addr.Used, m.Addr.Avail, true
		q.mu.Unlock()
	case reqSetVringBase:
		q := d.q[m.Index]
		q.stopRing()
		q.mu.Lock()
		q.base = uint16(m.Num)
		q.mu.Unlock()
	case reqGetVringBase:
		q := d.q[m.Index]
		q.stopRing()
		q.mu.Lock()
		base := q.base
		q.enabled = false
		closeFile(&q.kick)
		closeFile(&q.call)
		q.mu.Unlock()
		buf := appendHeader(nil, header{Request: h.Request, Flags: flagVersion1 | flagReply, Size: 8})
		buf = append(buf, byte(m.Index), byte(m.Index>>8), byte(m.Index>>16), byte(m.Index>>24),
			byte(base), byte(base>>8), 0, 0)
		_, err := d.conn.Write(buf)
		return err
	case reqSetVringKick, reqSetVringCall, reqSetVringErr:
		var f *os.File
		if !m.NoFD {
			if err := unix.SetNonblock(fds[0], true); err != nil {
				return err
			}
			f = os.NewFile(uintptr(fds[0]), "vring-fd")
			claimed = 1
		}
		if err := d.setVringFile(m, f); err != nil {
			return err
		}
	case reqSetVringEnable:
		q := d.q[m.Index]
		q.stopRing()
		q.mu.Lock()
		q.enabled = m.Num != 0
		q.mu.Unlock()
		if err := d.startAll(); err != nil {
			return err
		}
	}
	return d.ack(h)
}

func (d *Device) setVringFile(m message, f *os.File) error {
	q := d.q[m.Index]
	if m.Req == reqSetVringErr {
		if f != nil {
			_ = f.Close()
		}
		return nil
	}
	if m.Req == reqSetVringKick && f == nil {
		return fmt.Errorf("vhostnet: polling mode (no kick fd) unsupported")
	}
	q.stopRing()
	q.mu.Lock()
	if m.Req == reqSetVringKick {
		closeFile(&q.kick)
		q.kick = f
		d.wg.Add(1)
		go d.pumpKicks(q, f)
	} else {
		closeFile(&q.call)
		q.call = f
	}
	q.mu.Unlock()
	return d.startAll()
}

// pumpKicks turns eventfd counter reads into channel tokens so ring work can
// select on stop and shutdown; closing f ends the goroutine.
func (d *Device) pumpKicks(q *queue, f *os.File) {
	defer d.wg.Done()
	var b [8]byte
	for {
		if _, err := f.Read(b[:]); err != nil {
			return
		}
		q.poke()
	}
}

func (d *Device) swapMem(t *memTable) {
	old := d.mem
	d.mem = t
	d.memRestarts.Add(1)
	for _, r := range old.regions {
		keep := false
		for _, n := range t.regions {
			if n == r {
				keep = true
				break
			}
		}
		if !keep {
			r.unmap()
		}
	}
}

func (d *Device) removeRegion(r regionDesc) error {
	table := d.mem.clone()
	for i, have := range table.regions {
		if have.desc.GPA == r.GPA && have.desc.Size == r.Size && have.desc.UAddr == r.UAddr {
			table.regions = append(table.regions[:i], table.regions[i+1:]...)
			d.swapMem(table)
			return nil
		}
	}
	return fmt.Errorf("vhostnet: REM_MEM_REG matches no region")
}
