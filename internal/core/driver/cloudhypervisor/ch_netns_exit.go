package cloudhypervisor

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

// ExitInfo describes how a CH runtime process exited.
type ExitInfo struct {
	Signal int
	Code   int
}

// lastExits keeps the last ExitInfo per sandbox id so RuntimeExit still
// answers after the runtime leaves d.nets. Cleared by ForgetRuntimeExit and
// when a new runtime starts for the id.
var lastExits sync.Map // sandbox id string -> ExitInfo

// ForgetRuntimeExit drops the retained exit record for id.
func ForgetRuntimeExit(id string) { lastExits.Delete(id) }

// exitLikeChild makes the netns helper die the way CH did. A signaled CH is
// mirrored by re-raising the same signal on the helper with the default
// disposition, so the parent's WaitStatus shows Signaled() and a real CH exit
// code of 129..192 stays distinguishable. If the signal cannot take effect
// (e.g. ignored for PID 1), it falls back to exit code 128+signo.
func exitLikeChild(ws syscall.WaitStatus) {
	if ws.Signaled() {
		sig := ws.Signal()
		signal.Reset(sig)
		_ = syscall.Kill(os.Getpid(), sig)
		time.Sleep(500 * time.Millisecond)
		os.Exit(128 + int(sig))
	}
	os.Exit(ws.ExitStatus())
}

// decodeChildExit maps the helper's own wait status to an ExitInfo.
func decodeChildExit(ws syscall.WaitStatus) ExitInfo {
	if ws.Signaled() {
		return ExitInfo{Signal: int(ws.Signal())}
	}
	return ExitInfo{Code: ws.ExitStatus()}
}

// RuntimeExit reports the recorded exit of sandbox id's CH process, including
// after the runtime was removed from d.nets. False when none has exited.
func (d *CHDriver) RuntimeExit(id string) (ExitInfo, bool) {
	sid, err := domain.ParseSandboxID(id)
	if err != nil {
		return ExitInfo{}, false
	}
	d.mu.Lock()
	ns := d.nets[sid]
	d.mu.Unlock()
	if ns != nil && ns.rt != nil {
		ns.rt.exitMu.Lock()
		info, ok := ns.rt.exit, ns.rt.exitKnown
		ns.rt.exitMu.Unlock()
		if ok {
			return info, true
		}
	}
	if v, ok := lastExits.Load(sid.String()); ok {
		return v.(ExitInfo), true
	}
	return ExitInfo{}, false
}
