package image

import (
	"syscall"
	"time"
)

func statMtime(st *syscall.Stat_t) time.Time { return time.Unix(st.Mtimespec.Sec, st.Mtimespec.Nsec) }
