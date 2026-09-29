//go:build !linux

package cloudhypervisor

import (
	"errors"
	"io"
	"path/filepath"
)

func VhostSocketPath(dir, id string) string { return filepath.Join(dir, "vhost-"+id+".sock") }

func startVhostSlot(string) (io.ReadWriteCloser, error) {
	return nil, errors.New("cloudhypervisor: vhost-user net requires linux")
}
