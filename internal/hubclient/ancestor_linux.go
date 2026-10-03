//go:build linux

package hubclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultAgentComms are the process names treated as agent processes.
var DefaultAgentComms = []string{"claude", "codex"}

// ProcRoot is the procfs mount point; tests override it.
var ProcRoot = "/proc"

const maxAncestorDepth = 64

// FindAgentAncestor walks the parent chain of start, beginning at its
// parent, and returns the first process whose comm is in comms
// (DefaultAgentComms when comms is empty).
func FindAgentAncestor(start int, comms []string) (int, string, error) {
	if len(comms) == 0 {
		comms = DefaultAgentComms
	}
	pid, err := procPPID(start)
	if err != nil {
		return 0, "", err
	}
	seen := map[int]bool{start: true}
	for depth := 0; depth < maxAncestorDepth && pid > 1; depth++ {
		if seen[pid] {
			return 0, "", fmt.Errorf("agent ancestor: cycle at pid %d", pid)
		}
		seen[pid] = true
		comm, err := procComm(pid)
		if err != nil {
			return 0, "", err
		}
		for _, c := range comms {
			if comm == c {
				return pid, comm, nil
			}
		}
		if pid, err = procPPID(pid); err != nil {
			return 0, "", err
		}
	}
	return 0, "", errors.New("agent ancestor: not found")
}

func procComm(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join(ProcRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// procPPID parses /proc/<pid>/stat; comm may contain spaces and parens, so
// fields are located after the last ')'.
func procPPID(pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join(ProcRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("malformed stat for pid %d", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("malformed stat for pid %d", pid)
	}
	return strconv.Atoi(f[1])
}
