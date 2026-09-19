//go:build !linux

package builder

/** memoryBackedFS is conservative off Linux: any cross-device staging is treated as memory-backed. */
func memoryBackedFS(string) (bool, error) { return true, nil }
