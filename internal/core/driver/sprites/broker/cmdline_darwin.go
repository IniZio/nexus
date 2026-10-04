package broker

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

// processCmdline reads argv via sysctl kern.procargs2.
func processCmdline(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, errors.New("short procargs2")
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	// Skip exec path, then the NUL padding before argv[0].
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil, errors.New("malformed procargs2")
	}
	b = bytes.TrimLeft(b[i:], "\x00")
	parts := bytes.Split(b, []byte{0})
	if argc <= 0 || len(parts) < argc {
		return nil, errors.New("malformed procargs2")
	}
	out := make([]string, argc)
	for k := 0; k < argc; k++ {
		out[k] = string(parts[k])
	}
	return out, nil
}
