//go:build linux

package oomattr

import (
	"os"
	"path"
	"strconv"
	"strings"
)

func takeFrom(root string) Snapshot {
	var s Snapshot
	if b, err := os.ReadFile(root + "/proc/sys/kernel/random/boot_id"); err == nil {
		s.BootID = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(root + "/proc/vmstat"); err == nil {
		s.VmstatOOMKill = counter(string(b), "oom_kill")
	}
	b, err := os.ReadFile(root + "/proc/self/cgroup")
	if err != nil {
		return s
	}
	rel := ""
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "0::") {
			rel = path.Clean("/" + strings.TrimPrefix(line, "0::"))
			break
		}
	}
	if rel == "" {
		return s
	}
	first := true
	for p := rel; p != "/" && p != "."; p = path.Dir(p) {
		ev, err := os.ReadFile(root + "/sys/fs/cgroup" + p + "/memory.events")
		if err != nil {
			first = false
			continue
		}
		text := string(ev)
		kills := counter(text, "oom_kill") + counter(text, "oom_group_kill")
		if first {
			s.ScopeOOMKill = kills
			s.ScopeOOM = counter(text, "oom")
		} else {
			s.AncestorOOMKill += kills
			s.AncestorOOM += counter(text, "oom")
		}
		first = false
	}
	return s
}

// counter returns the value of the "key value" line, or 0.
func counter(text, key string) int64 {
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == key {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			return n
		}
	}
	return 0
}
