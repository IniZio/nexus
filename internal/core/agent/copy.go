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
	// ExpectedBytes is the authoritative byte count of the payload for
	// non-directory PUSH transfers. Declared as a pointer so nil (absent)
	// is distinguishable from &0 (declared empty file). The guest fails
	// closed when the field is nil — callers must set it for every single-file
	// PUSH, including for size-0 files. Not set for directory PUSH transfers.
	ExpectedBytes *int64
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
		Direction:     opts.Direction,
		GuestPath:     opts.GuestPath,
		IsDirectory:   opts.IsDirectory,
		ExpectedBytes: opts.ExpectedBytes,
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
	return copyPull(rd, opts.Dst, resp.DeclaredBytes, opts.IsDirectory, opts.GuestPath)
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

// copyPull reads Data(Stdout) frames from the guest and writes the bytes to
// dst, returning once the guest sends an Exit frame.
//
// For single-file pulls (isDirectory false), declaredBytes must be non-nil:
// it is the authoritative file size declared by the agent in CopyResponse via
// proto3 field presence (optional int64). nil means the agent did not declare
// — fail closed. A pointer to 0 is valid and means an empty file. A
// byte-count mismatch against the pointed-to value is also an error, guarding
// against transport truncation (e.g. the 32 MiB vsock cap in buildkit).
//
// For directory pulls (isDirectory true), the received bytes form a tar
// archive. They are piped through archive/tar inline to detect truncated entry
// bodies: archive/tar returns io.ErrUnexpectedEOF when a body is shorter than
// its declared size in the tar header, which surfaces as an error here.
func copyPull(rd *wire.Reader, dst io.Writer, declaredBytes *int64, isDirectory bool, guestPath string) error {
	// For directory pulls wire the received bytes through an inline tar
	// validator so truncated entry bodies surface as errors rather than silent
	// short files on the host.
	var (
		tarPW    *io.PipeWriter
		tarErrCh chan error
	)
	writeDst := dst
	if isDirectory {
		var tarPR *io.PipeReader
		tarPR, tarPW = io.Pipe()
		tarErrCh = make(chan error, 1)
		go func() { tarErrCh <- validateTarStream(tarPR) }()
		if dst != nil {
			writeDst = io.MultiWriter(dst, tarPW)
		} else {
			writeDst = tarPW
		}
	}

	var n int64
	for {
		frame, err := rd.ReadFrame()
		if err != nil {
			if tarPW != nil {
				tarPW.CloseWithError(err)
				<-tarErrCh
			}
			return fmt.Errorf("agent: copy pull: read frame: %w", err)
		}
		switch frame.Type {
		case wire.FrameData:
			if frame.Data != nil && frame.Data.Tag == wire.StreamStdout && writeDst != nil {
				nb, werr := writeDst.Write(frame.Data.Payload)
				n += int64(nb)
				if werr != nil {
					if tarPW != nil {
						tarPW.CloseWithError(werr)
						<-tarErrCh
					}
					return fmt.Errorf("agent: copy pull: write dst: %w", werr)
				}
			}
		case wire.FrameExit:
			if isDirectory {
				tarPW.Close()
				if tarErr := <-tarErrCh; tarErr != nil {
					return fmt.Errorf("agent: copy pull %q: tar validation: %w", guestPath, tarErr)
				}
			} else {
				// nil = agent did not set the field (not declared) — fail closed.
				// A pointer to 0 is valid: it means the agent declared an empty file.
				if declaredBytes == nil {
					return fmt.Errorf("agent: copy pull %q: DeclaredBytes absent — agent must declare authoritative file size for single-file PULL", guestPath)
				}
				if n != *declaredBytes {
					return fmt.Errorf("agent: copy pull %q: received %d bytes, expected %d — transfer truncated", guestPath, n, *declaredBytes)
				}
			}
			return nil
		}
	}
}

// validateTarStream reads through every tar entry in r and discards entry
// data. It exists solely to verify archive integrity: archive/tar returns
// io.ErrUnexpectedEOF when it cannot read the full body declared in a tar
// header, so a truncated receive will surface as an error rather than a
// silently short file on the host.
func validateTarStream(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		_, err := tr.Next() // auto-drains the previous entry's body
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
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
