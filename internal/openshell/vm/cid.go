package vm

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	cidMin uint32 = 10
	cidMax uint32 = 4094
)

func allocateCID(stateDir string) (uint32, error) {
	counterFile := filepath.Join(stateDir, "cid-counter")

	f, err := os.OpenFile(counterFile, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data := make([]byte, 20)
	n, err := f.Read(data)
	if err != nil && err.Error() != "EOF" {
		return 0, err
	}

	var current uint32 = cidMin
	if n > 0 {
		str := string(data[:n])
		if val, err := strconv.ParseUint(str, 10, 32); err == nil {
			current = uint32(val)
		}
	}

	next := current
	if next < cidMin {
		next = cidMin
	} else if next > cidMax {
		next = cidMin
	}

	if _, err := f.Seek(0, 0); err != nil {
		return 0, err
	}
	if err := f.Truncate(0); err != nil {
		return 0, err
	}

	nextCounter := next + 1
	if nextCounter > cidMax {
		nextCounter = cidMin
	}
	if _, err := f.WriteString(strconv.FormatUint(uint64(nextCounter), 10)); err != nil {
		return 0, err
	}

	return next, nil
}

func releaseCID(_ string, _ uint32) {}
