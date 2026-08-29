package main

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

// pendingCopy is a negotiated file-transfer operation waiting for a
// data-plane connection to arrive carrying the transfer_id as its SessionID.
type pendingCopy struct {
	transferID    string
	direction     agentpb.CopyDirection
	guestPath     string
	isDirectory   bool
	expectedBytes *int64 // nil = not declared (fail closed); &0 = declared empty file
}

// CopyTable stores pending copy operations keyed by transfer_id.
type CopyTable struct {
	mu      sync.RWMutex
	pending map[string]*pendingCopy
}

func newCopyTable() *CopyTable {
	return &CopyTable{pending: make(map[string]*pendingCopy)}
}

func (t *CopyTable) add(cp *pendingCopy) {
	t.mu.Lock()
	t.pending[cp.transferID] = cp
	t.mu.Unlock()
}

func (t *CopyTable) get(id string) (*pendingCopy, bool) {
	t.mu.RLock()
	cp, ok := t.pending[id]
	t.mu.RUnlock()
	return cp, ok
}

func (t *CopyTable) delete(id string) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

// Copy negotiates a file-transfer operation (control-plane RPC only).
// The returned transfer_id is used as the SessionID in the data-plane handshake.
//
// For single-file PULL, the agent stats the file before minting the transfer
// handle so that it can declare the authoritative size in DeclaredBytes. The
// host rejects the transfer if the received byte count differs — a fail-closed
// guard against transport truncation.
func (cs *controlServer) Copy(_ context.Context, req *agentpb.CopyRequest) (*agentpb.CopyResponse, error) {
	if req.GuestPath == "" {
		return nil, status.Error(codes.InvalidArgument, "guest_path required")
	}

	// Stat before minting the transfer handle so that an ENOENT returns an
	// error immediately rather than leaving an orphaned pending-copy slot.
	// DeclaredBytes is left nil for directory PULL and all PUSH transfers;
	// the host distinguishes nil (not declared) from 0 (empty file declared).
	var declaredBytes *int64
	if req.Direction == agentpb.CopyDirection_COPY_DIRECTION_PULL && !req.IsDirectory {
		fi, err := os.Stat(req.GuestPath)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "copy pull: stat %s: %v", req.GuestPath, err)
		}
		size := fi.Size()
		declaredBytes = &size
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, status.Errorf(codes.Internal, "rand: %v", err)
	}
	transferID := hex.EncodeToString(b)
	cs.a.copies.add(&pendingCopy{
		transferID:    transferID,
		direction:     req.Direction,
		guestPath:     req.GuestPath,
		isDirectory:   req.IsDirectory,
		expectedBytes: req.ExpectedBytes,
	})
	return &agentpb.CopyResponse{TransferId: transferID, DeclaredBytes: declaredBytes}, nil
}

// handleCopyTransfer handles a data-plane connection for a file transfer.
// The host uses the transfer_id from the Copy RPC as the handshake SessionID.
func (a *Agent) handleCopyTransfer(_ net.Conn, r *wire.Reader, w *wire.Writer, cp *pendingCopy) {
	defer a.copies.delete(cp.transferID)

	_ = w.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive})

	var transferErr error
	switch cp.direction {
	case agentpb.CopyDirection_COPY_DIRECTION_PULL:
		transferErr = pullTransfer(cp.guestPath, cp.isDirectory, w)
	case agentpb.CopyDirection_COPY_DIRECTION_PUSH:
		transferErr = pushTransfer(cp.guestPath, cp.isDirectory, r, cp.expectedBytes)
	}

	code := int32(0)
	if transferErr != nil {
		code = 1
	}
	_ = w.WriteExit(wire.Exit{Code: code})
}

// pushTransfer receives the archive stream from the host (as Data(Stdout)
// frames terminated by an Exit frame) and writes or extracts it to guestPath.
// For a single file the raw bytes are written directly; for a directory the
// stream is expected to be a tar archive that is extracted under guestPath.
// expectedBytes, when non-nil, is a fail-closed size guard for single-file
// pushes: the transfer is rejected if the received byte count differs.
// nil = host did not declare the size — fail closed (rejected immediately).
func pushTransfer(guestPath string, isDir bool, r *wire.Reader, expectedBytes *int64) error {
	pr, pw := io.Pipe()
	go func() {
		for {
			f, err := r.ReadFrame()
			if err != nil {
				pw.CloseWithError(fmt.Errorf("push: read frame: %w", err))
				return
			}
			switch f.Type {
			case wire.FrameData:
				if f.Data != nil && f.Data.Tag == wire.StreamStdout {
					if _, werr := pw.Write(f.Data.Payload); werr != nil {
						pw.CloseWithError(werr)
						return
					}
				}
			case wire.FrameExit:
				pw.Close()
				return
			}
		}
	}()
	defer pr.Close() // unblocks the goroutine if the consumer returns early
	if isDir {
		return pushDir(guestPath, pr)
	}
	return pushFile(guestPath, pr, expectedBytes)
}

// pushFile creates (or truncates) guestPath and writes r into it, creating
// parent directories as needed. expectedBytes must be non-nil: it is a
// fail-closed guard against transport truncation (e.g. the 32 MiB vsock cap
// observed in in-guest buildkit exports). nil means the host did not declare
// the size — the guest fails closed immediately so the guard cannot be silently
// disabled. A pointer to 0 is valid and means an empty file.
func pushFile(guestPath string, r io.Reader, expectedBytes *int64) error {
	if expectedBytes == nil {
		return fmt.Errorf("push file: expectedBytes absent — host must declare authoritative source size for single-file PUSH")
	}
	if err := os.MkdirAll(filepath.Dir(guestPath), 0o755); err != nil {
		return fmt.Errorf("push file: mkdir parent: %w", err)
	}
	f, err := os.Create(guestPath)
	if err != nil {
		return fmt.Errorf("push file: create: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return fmt.Errorf("push file: write: %w", err)
	}
	if n != *expectedBytes {
		return fmt.Errorf("push file: received %d bytes, expected %d — transfer truncated", n, *expectedBytes)
	}
	return nil
}

// pushDir extracts a tar archive from r into root, creating directories and
// files as needed. Path traversal entries are rejected.
func pushDir(root string, r io.Reader) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("push dir: mkdir root: %w", err)
	}
	cleanRoot := filepath.Clean(root)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("push dir: next tar entry: %w", err)
		}
		target := filepath.Join(root, hdr.Name)
		cleanTarget := filepath.Clean(target)
		// Guard against path traversal (e.g. "../../../etc/passwd" in tar).
		if cleanTarget != cleanRoot && !strings.HasPrefix(cleanTarget, cleanRoot+string(filepath.Separator)) {
			return fmt.Errorf("push dir: path traversal in tar entry %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("push dir: mkdir %q: %w", hdr.Name, err)
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("push dir: mkdir parent of %q: %w", hdr.Name, err)
			}
			f, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("push dir: create %q: %w", hdr.Name, err)
			}
			_, copyErr := io.Copy(f, tr)
			f.Close()
			if copyErr != nil {
				return fmt.Errorf("push dir: write %q: %w", hdr.Name, copyErr)
			}
			if err := os.Chmod(target, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("push dir: chmod %q: %w", hdr.Name, err)
			}
		}
	}
}

// pullTransfer archives guestPath and streams it as Data(Stdout) frames.
func pullTransfer(guestPath string, isDir bool, w *wire.Writer) error {
	if !isDir {
		return pullFile(guestPath, w)
	}
	return pullDir(guestPath, w)
}

func pullFile(path string, w *wire.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, wire.MaxDataPayload)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if werr := w.WriteData(wire.StreamStdout, buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func pullDir(root string, w *wire.Writer) error {
	pr, pw := io.Pipe()
	tw := tar.NewWriter(pw)
	go func() {
		ferr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			hdr.Name = rel
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

	buf := make([]byte, wire.MaxDataPayload)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			if werr := w.WriteData(wire.StreamStdout, buf[:n]); werr != nil {
				_ = pr.CloseWithError(werr)
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
