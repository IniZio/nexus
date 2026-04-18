package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type lockHandle struct {
	path string
}

func acquireLock(path string) (*lockHandle, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
	_ = file.Close()
	return &lockHandle{path: path}, nil
}

func (l *lockHandle) Close() error {
	if l == nil || l.path == "" {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove lock: %w", err)
	}
	return nil
}
