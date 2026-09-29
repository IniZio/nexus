//go:build linux

package cloudhypervisor

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor/vhostnet"
)

// VhostSocketPath returns the vhost-user net socket path for a sandbox given
// the netns control directory.
func VhostSocketPath(dir, id string) string {
	return filepath.Join(dir, "vhost-"+id+".sock")
}

// vhostSlot is the NIC side of tapPump in vhost-user mode. It exposes the
// vhostnet.Device of the current master connection as one stable
// io.ReadWriteCloser: reads block until CH has connected, frames written
// before that are dropped like a tap with no reader.
type vhostSlot struct {
	ln   *net.UnixListener
	sock string

	mu     sync.Mutex
	dev    *vhostnet.Device
	gen    chan struct{}
	closed bool
	wg     sync.WaitGroup
}

// startVhostSlot listens on path (0600, in a 0700 directory) and accepts
// master connections in the background.
func startVhostSlot(path string) (*vhostSlot, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", dir, err)
	}
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	s := &vhostSlot{ln: ln, sock: path, gen: make(chan struct{})}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

func (s *vhostSlot) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.AcceptUnix()
		if err != nil {
			return
		}
		dev, err := vhostnet.Serve(c, vhostnet.Config{})
		if err != nil {
			c.Close()
			continue
		}
		s.install(dev)
	}
}

func (s *vhostSlot) install(dev *vhostnet.Device) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		dev.Close()
		return
	}
	old, oldGen := s.dev, s.gen
	s.dev, s.gen = dev, make(chan struct{})
	s.mu.Unlock()
	close(oldGen)
	if old != nil {
		old.Close()
	}
}

func (s *vhostSlot) current() (*vhostnet.Device, chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dev, s.gen, s.closed
}

// Read returns one guest-transmitted frame, riding over master reconnects.
func (s *vhostSlot) Read(p []byte) (int, error) {
	for {
		dev, gen, closed := s.current()
		if closed {
			return 0, io.EOF
		}
		if dev == nil {
			<-gen
			continue
		}
		n, err := dev.Read(p)
		if errors.Is(err, io.EOF) {
			<-gen
			continue
		}
		return n, err
	}
}

// Write delivers one frame to the guest; with no master connected it drops.
func (s *vhostSlot) Write(p []byte) (int, error) {
	dev, _, closed := s.current()
	if closed || dev == nil {
		return len(p), nil
	}
	return dev.Write(p)
}

// Close stops accepting, closes the device and removes the socket.
func (s *vhostSlot) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	dev, gen := s.dev, s.gen
	s.mu.Unlock()
	close(gen)
	_ = s.ln.Close()
	if dev != nil {
		dev.Close()
	}
	s.wg.Wait()
	_ = os.Remove(s.sock)
	return nil
}
