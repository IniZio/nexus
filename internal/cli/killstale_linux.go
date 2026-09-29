//go:build linux

package cli

import (
	"log/slog"
	"syscall"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
)

// killStaleNetnsGroup SIGKILLs the netns child's process group recorded on sb
// when it is still the same process (pgid and start time match). It covers a
// VM whose CH API socket is gone: CHDriver.Stop treats an absent socket as
// "nothing to do" and would leave the VM running. The start-time check keeps a
// recycled pid from being signalled.
func killStaleNetnsGroup(sb domain.Sandbox) {
	if sb.NetnsChildPID <= 0 || sb.NetnsChildPGID <= 0 || sb.NetnsChildStartTime == 0 {
		return
	}
	st, err := cloudhypervisor.ReadProcStat(sb.NetnsChildPID)
	if err != nil || st.StartTime != sb.NetnsChildStartTime || st.PGID != sb.NetnsChildPGID {
		return
	}
	if err := syscall.Kill(-sb.NetnsChildPGID, syscall.SIGKILL); err != nil {
		slog.Warn("sandbox: kill stale netns group", "pgid", sb.NetnsChildPGID, "err", err)
	}
}
