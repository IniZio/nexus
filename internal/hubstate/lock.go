// Package hubstate is the file-backed coordination state for the session hub:
// per-domain flock files, atomic writes, and pid/starttime/boot_id liveness.
package hubstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	ErrUnsupported = errors.New("hubstate: unsupported on this platform")
	ErrLockTimeout = errors.New("hubstate: lock wait timed out")
	ErrBadFS       = errors.New("hubstate: state dir is on a non-local filesystem")
)

var domains = []string{"seats", "sessions", "mail", "leases", "budget"}

// Filesystem magic numbers refused for the state dir.
var badFS = map[int64]string{
	0x65735546: "fuse/virtiofs",
	0x01021997: "9p",
	0x6969:     "nfs",
	0x517B:     "smb",
	0xFF534D42: "cifs",
	0xFE534D42: "smb2",
}

var lockWait = 5 * time.Second

type Store struct {
	root string
}

// DefaultRoot is $XDG_STATE_HOME/nexus/hub, falling back to ~/.local/state.
func DefaultRoot() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "nexus", "hub"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "state", "nexus", "hub"), nil
}

// Open creates the layout under root ("" means DefaultRoot) and refuses
// non-local filesystems.
func Open(root string) (*Store, error) {
	return open(root, realStatfsType)
}

func open(root string, statfsType func(string) (int64, error)) (*Store, error) {
	if root == "" {
		r, err := DefaultRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}
	for _, d := range domains {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, err
		}
	}
	typ, err := statfsType(root)
	if err != nil {
		return nil, fmt.Errorf("hubstate: statfs %s: %w", root, err)
	}
	if name, bad := badFS[typ]; bad {
		return nil, fmt.Errorf("%w: %s (%s)", ErrBadFS, root, name)
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) dir(domain string) string { return filepath.Join(s.root, domain) }

// Lock takes the domain's flock, waiting up to 5s. The kernel releases it on
// holder death. The returned func unlocks.
func (s *Store) Lock(domain string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.root, domain+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrLockTimeout
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// validName rejects names that could escape the domain directory.
func validName(n string) error {
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00") || strings.HasPrefix(n, ".tmp-") {
		return fmt.Errorf("hubstate: invalid name %q", n)
	}
	return nil
}
