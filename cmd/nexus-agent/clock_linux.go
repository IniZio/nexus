package main

import (
	"time"

	"golang.org/x/sys/unix"
)

func setRealtimeClock(t time.Time) error {
	ts := unix.NsecToTimespec(t.UnixNano())
	return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
}
