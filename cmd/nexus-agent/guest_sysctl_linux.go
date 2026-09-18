//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

type inotifyTarget struct {
	rel   string
	floor int64
}

var guestInotifyTargets = []inotifyTarget{
	{rel: "fs/inotify/max_user_watches", floor: 524288},
	{rel: "fs/inotify/max_user_instances", floor: 1024},
	{rel: "fs/inotify/max_queued_events", floor: 65536},
}

func applyGuestSysctls(procSysRoot string) error {
	var firstErr error
	for _, t := range guestInotifyTargets {
		if err := applyInotifySysctl(procSysRoot+"/"+t.rel, t.floor); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func applyInotifySysctl(path string, floor int64) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cur, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err == nil && cur >= floor {
		return nil
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(floor, 10)+"\n"), 0o644)
}
