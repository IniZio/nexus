//go:build linux

package hub

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// fsMagic -> name for filesystems where SQLite WAL is unsafe. virtiofs reports
// the FUSE magic.
var refusedFS = map[int64]string{
	0x6969:     "nfs",
	0x01021997: "9p",
	0x65735546: "fuse/virtiofs",
	0x517B:     "smb",
	0xFF534D42: "cifs",
}

func refusedFSName(magic int64) (string, bool) {
	n, ok := refusedFS[magic]
	return n, ok
}

func checkFS(dir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("hub: statfs %q: %w", dir, err)
	}
	if n, bad := refusedFSName(int64(st.Type)); bad {
		return fmt.Errorf("hub: %q is on %s; SQLite WAL needs shared memory and is unsafe there", dir, n)
	}
	return nil
}
