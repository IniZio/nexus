package builder

import "syscall"

/**
 * memoryBackedFS reports whether path lives on a RAM-backed filesystem
 * (tmpfs or ramfs), the one place a cross-device staging copy must not go.
 */
func memoryBackedFS(path string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, err
	}
	const tmpfsMagic, ramfsMagic = 0x01021994, 0x858458f6
	return st.Type == tmpfsMagic || st.Type == ramfsMagic, nil
}
