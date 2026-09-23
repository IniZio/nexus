package main

import (
	"context"
	"encoding/binary"
	"net"
	"sync"

	"github.com/creack/pty"

	"github.com/IniZio/nexus/internal/core/agent/wire"
)

// serveData accepts connections on the data-plane listener and dispatches
// each to handleDataConn in its own goroutine.
func (a *Agent) serveData(ctx context.Context) error {
	for {
		conn, err := a.dataLis.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go a.handleDataConn(ctx, conn)
	}
}

// handleDataConn drives a single data-plane connection.
//
// Protocol:
//  1. Read Handshake → look up session (or pending copy).
//  2. Write HandshakeAck (Alive or Exited).
//  3. Stream ring → Data(Stdout) frames, concurrently forward
//     Data(Stdin) frames to the PTY/pipe and apply Winsize frames.
//  4. After ring is drained and done, send Exit{code}.
func (a *Agent) handleDataConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	frame, err := r.ReadFrame()
	if err != nil || frame.Type != wire.FrameHandshake {
		return
	}
	hs := frame.Handshake

	// Try session lookup, then copy lookup.
	sess, ok := a.sessions.get(hs.SessionID)
	if !ok {
		cp, cpOk := a.copies.get(hs.SessionID)
		if !cpOk {
			// Unknown session: reject.
			_ = w.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckExited, ExitCode: -1})
			return
		}
		a.handleCopyTransfer(conn, r, w, cp)
		return
	}

	oldest := sess.ring.OldestOffset()
	from := hs.ResumeFromOffset
	if sess.tagged {
		from = sess.ring.SnapToRecordBoundary(from)
	} else if from < oldest {
		from = oldest
	}
	var bytesLost uint64
	if from > hs.ResumeFromOffset {
		bytesLost = from - hs.ResumeFromOffset
	}

	alreadyExited := sess.exited.Load()
	if alreadyExited {
		_ = w.WriteHandshakeAck(wire.HandshakeAck{
			Status:    wire.AckExited,
			ExitCode:  sess.exitCode.Load(),
			BytesLost: bytesLost,
		})
		readerID := sess.ring.AddReader(from)
		streamRingToWriter(w, sess.ring, readerID, from, sess.tagged)
		sess.ring.RemoveReader(readerID)
		_ = w.WriteExit(wire.Exit{Code: sess.exitCode.Load()})
		return
	}

	_ = w.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive, BytesLost: bytesLost})

	readerID := sess.ring.AddReader(from)
	sess.claimPendingReader()

	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		defer sess.ring.RemoveReader(readerID)
		streamRingToWriter(w, sess.ring, readerID, from, sess.tagged)
		_ = w.WriteExit(wire.Exit{Code: sess.exitCode.Load()})
	}()

	// stdinCloseOnce guards sess.stdinW.Close() so FrameStdinClose and
	// teardown at most close the pipe once, preventing a double-close error.
	var stdinCloseOnce sync.Once
	closeStdinW := func() {
		if sess.stdinW != nil {
			stdinCloseOnce.Do(func() { sess.stdinW.Close() })
		}
	}

	// Inbound: Stdin → PTY/pipe, Winsize → pty resize.
	go func() {
		defer closeStdinW() // backstop: close stdinW when inbound loop exits
		for {
			select {
			case <-doneCh:
				return
			default:
			}
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			switch f.Type {
			case wire.FrameData:
				if f.Data == nil || f.Data.Tag != wire.StreamStdin {
					continue
				}
				if sess.ptmx != nil {
					_, _ = sess.ptmx.Write(f.Data.Payload)
				} else if sess.stdinW != nil {
					_, _ = sess.stdinW.Write(f.Data.Payload)
				}
			case wire.FrameStdinClose:
				// Host stdin reached EOF: close the process stdin pipe so
				// the child process sees EOF and can proceed/exit.
				closeStdinW()
			case wire.FrameWinsize:
				if sess.ptmx != nil && f.Winsize != nil {
					ws := f.Winsize
					_ = pty.Setsize(sess.ptmx, &pty.Winsize{
						Rows: ws.Rows,
						Cols: ws.Cols,
						X:    ws.XPixels,
						Y:    ws.YPixels,
					})
				}
			case wire.FrameExit:
				return
			}
		}
	}()

	<-doneCh
}

func streamRingToWriter(w *wire.Writer, ring *Ring, readerID uint64, from uint64, tagged bool) {
	off := from
	var carry []byte
	for {
		chunk, newOff, done, _ := ring.WaitNextCursored(readerID, off)
		off = newOff
		if len(chunk) > 0 {
			if tagged {
				var data []byte
				if len(carry) > 0 {
					data = append(carry, chunk...)
					carry = nil
				} else {
					data = chunk
				}
				for len(data) >= 5 {
					tag := wire.StreamTag(data[0])
					plen := int(binary.BigEndian.Uint32(data[1:5]))
					if plen > maxTaggedPayload {
						return
					}
					if len(data) < 5+plen {
						break
					}
					if err := w.WriteData(tag, data[5:5+plen]); err != nil {
						return
					}
					data = data[5+plen:]
				}
				if len(data) > 0 {
					carry = append(carry[:0], data...)
				}
			} else {
				if err := w.WriteData(wire.StreamStdout, chunk); err != nil {
					return
				}
			}
		}
		if done && len(chunk) == 0 {
			return
		}
	}
}
