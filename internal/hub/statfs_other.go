//go:build !linux

package hub

func refusedFSName(magic int64) (string, bool) { return "", false }

func checkFS(dir string) error { return nil }
