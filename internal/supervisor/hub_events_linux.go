//go:build linux

package supervisor

import (
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/oomattr"
)

// runtimeExiter is implemented by drivers that record how the VM process exited.
type runtimeExiter interface {
	RuntimeExit(id string) (cloudhypervisor.ExitInfo, bool)
}

// exitSignal returns the recorded exit of id, or the zero Signal when unknown.
func exitSignal(drv any, id string) oomattr.Signal {
	if re, ok := drv.(runtimeExiter); ok {
		if info, ok := re.RuntimeExit(id); ok {
			return oomattr.Signal{Signal: info.Signal, Code: info.Code}
		}
	}
	return oomattr.Signal{}
}
