package agent

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

// CopyOptions configures a [Client.Copy] call.
type CopyOptions struct {
	// Direction specifies whether the host pushes to the guest (PUSH) or
	// receives from the guest (PULL).
	Direction agentpb.CopyDirection
	// GuestPath is the path inside the guest for the transfer.
	GuestPath string
	// IsDirectory indicates whether GuestPath refers to a directory.
	IsDirectory bool
	// Src is read for PUSH transfers (host → guest). May be nil for PULL.
	Src io.Reader
	// Dst receives archive bytes for PULL transfers (guest → host).
	// May be nil for PUSH.
	Dst io.Writer
}

// Copy negotiates a file-transfer operation with the guest over the split
// control/data plane.
//
// Metadata (direction, guest path) travels on the gRPC control plane; archive
// bytes flow on the data-plane connection identified by the transfer_id
// returned by the Copy RPC. The Handshake.SessionID is set to transfer_id so
// the guest can demultiplex the data connection to the correct transfer.
//
// For PUSH the host reads archive bytes from [CopyOptions.Src] and streams
// them to the guest as Data frames, then signals completion with an Exit
// frame. For PULL the host reads Data frames from the guest and writes archive
// bytes to [CopyOptions.Dst], returning once the guest sends an Exit frame.
//
// The guest is responsible for archiving and extracting; the host only moves
// raw bytes.
func (c *Client) Copy(ctx context.Context, opts CopyOptions) error {
	stub, cc, err := c.controlClient(ctx)
	if err != nil {
		return err
	}
	defer cc.Close()

	resp, err := stub.Copy(ctx, &agentpb.CopyRequest{
		Direction:   opts.Direction,
		GuestPath:   opts.GuestPath,
		IsDirectory: opts.IsDirectory,
	})
	if err != nil {
		return fmt.Errorf("agent: copy rpc: %w", err)
	}
	transferID := resp.GetTransferId()

	// Open the data-plane connection for the archive byte stream.
	dataConn, err := c.dialData(ctx)
	if err != nil {
		return err
	}
	defer dataConn.Close()

	wr := wire.NewWriter(dataConn)
	if err := wr.WriteHandshake(wire.Handshake{
		SessionID:        transferID,
		ResumeFromOffset: 0,
	}); err != nil {
		return fmt.Errorf("agent: copy: write handshake: %w", err)
	}

	rd := wire.NewReader(dataConn)
	ackFrame, err := rd.ReadFrame()
	if err != nil {
		return fmt.Errorf("agent: copy: read handshake ack: %w", err)
	}
	if ackFrame.HandshakeAck == nil {
		return fmt.Errorf("agent: copy: expected HandshakeAck, got frame type %d", ackFrame.Type)
	}

	if opts.Direction == agentpb.CopyDirection_COPY_DIRECTION_PUSH {
		return copyPush(wr, opts.Src)
	}
	return copyPull(rd, opts.Dst)
}

// copyPush reads archive bytes from src and streams them to the guest as
// Data(Stdout) frames, then signals end-of-archive with an Exit frame.
func copyPush(wr *wire.Writer, src io.Reader) error {
	if src == nil {
		return fmt.Errorf("agent: copy push: src reader is nil")
	}
	buf := make([]byte, wire.MaxDataPayload)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if werr := wr.WriteData(wire.StreamStdout, buf[:n]); werr != nil {
				return fmt.Errorf("agent: copy push: write data: %w", werr)
			}
		}
		if err == io.EOF {
			// Signal archive completion to the guest.
			return wr.WriteExit(wire.Exit{Code: 0})
		}
		if err != nil {
			return fmt.Errorf("agent: copy push: read src: %w", err)
		}
	}
}

// copyPull reads Data frames from the guest and writes archive bytes to dst,
// returning once the guest sends an Exit frame.
func copyPull(rd *wire.Reader, dst io.Writer) error {
	for {
		frame, err := rd.ReadFrame()
		if err != nil {
			return fmt.Errorf("agent: copy pull: read frame: %w", err)
		}
		switch frame.Type {
		case wire.FrameData:
			if dst != nil {
				if _, werr := dst.Write(frame.Data.Payload); werr != nil {
					return fmt.Errorf("agent: copy pull: write dst: %w", werr)
				}
			}
		case wire.FrameExit:
			return nil
		}
	}
}

// NewPushReader opens path and returns an [io.ReadCloser] that yields the
// bytes to stream in a PUSH transfer (host → guest).
//
// For a regular file the file itself is returned. For a directory an on-the-fly
// tar archive is produced; the caller must Close the reader to release pipe
// resources on early exit.
//
// Pass the returned reader as [CopyOptions.Src] with
// [agentpb.CopyDirection_COPY_DIRECTION_PUSH].
func NewPushReader(path string, isDir bool) (io.ReadCloser, error) {
	if !isDir {
		return os.Open(path)
	}
	return newDirTarReader(path), nil
}

// newDirTarReader walks root and writes a tar archive into a pipe; the read
// end is returned so the caller can stream the archive incrementally. The
// goroutine driving the walk closes the write end (with any error) when done.
func newDirTarReader(root string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		ferr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			hdr.Name = filepath.ToSlash(rel)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.IsDir() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(tw, f)
				return err
			}
			return nil
		})
		tw.Close()
		pw.CloseWithError(ferr)
	}()
	return pr
}
