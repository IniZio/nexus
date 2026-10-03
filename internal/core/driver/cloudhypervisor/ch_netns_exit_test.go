package cloudhypervisor

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestDecodeChildExit(t *testing.T) {
	cases := []struct {
		name string
		ws   syscall.WaitStatus
		want ExitInfo
	}{
		{"clean", syscall.WaitStatus(0), ExitInfo{}},
		{"code 3", syscall.WaitStatus(3 << 8), ExitInfo{Code: 3}},
		{"helper signaled", syscall.WaitStatus(syscall.SIGKILL), ExitInfo{Signal: 9}},
		{"real exit code 137 is not a signal", syscall.WaitStatus(137 << 8), ExitInfo{Code: 137}},
	}
	for _, c := range cases {
		if got := decodeChildExit(c.ws); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

// exitLikeChild re-raises CH's signal: run it in a subprocess and check the
// parent sees Signaled().
func TestExitLikeChildHelper(t *testing.T) {
	if os.Getenv("NX_EXIT_HELPER") == "" {
		t.Skip("subprocess helper")
	}
	sig, _ := strconv.Atoi(os.Getenv("NX_EXIT_SIG"))
	if sig > 0 {
		exitLikeChild(syscall.WaitStatus(sig))
	}
	code, _ := strconv.Atoi(os.Getenv("NX_EXIT_CODE"))
	exitLikeChild(syscall.WaitStatus(code << 8))
}

func runExitHelper(t *testing.T, env ...string) syscall.WaitStatus {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExitLikeChildHelper$")
	cmd.Env = append(os.Environ(), append([]string{"NX_EXIT_HELPER=1"}, env...)...)
	_ = cmd.Run()
	return cmd.ProcessState.Sys().(syscall.WaitStatus)
}

func TestExitLikeChild_ReraisesSignal(t *testing.T) {
	ws := runExitHelper(t, "NX_EXIT_SIG=9")
	if !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("want helper killed by SIGKILL, got %v", ws)
	}
	if got := decodeChildExit(ws); got != (ExitInfo{Signal: 9}) {
		t.Fatalf("decode: %+v", got)
	}
}

func TestExitLikeChild_RealHighExitCodeStaysCode(t *testing.T) {
	ws := runExitHelper(t, "NX_EXIT_CODE=140")
	if ws.Signaled() || ws.ExitStatus() != 140 {
		t.Fatalf("want exit 140, got %v", ws)
	}
	if got := decodeChildExit(ws); got != (ExitInfo{Code: 140}) {
		t.Fatalf("decode: %+v", got)
	}
}

// reapWatcher is the single waiter: the CH signal must land in waitStatus.
func TestReapWatcher_RecordsSignalStatus(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := newManagedProcess(cmd, cmd.Process.Pid, nil)
	_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-p.deathCh:
	case <-time.After(5 * time.Second):
		t.Fatal("deathCh not closed")
	}
	if !p.waitStatus.Signaled() || p.waitStatus.Signal() != syscall.SIGKILL {
		t.Fatalf("waitStatus lost signal: %v", p.waitStatus)
	}
}

func TestRuntimeExit_SurvivesNetsRemoval(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -9 $$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sid := domain.NewSandboxID()
	rt := &NetnsRuntime{cmd: cmd, deathCh: make(chan struct{}), sandboxID: sid.String()}
	d := &CHDriver{nets: map[domain.SandboxID]*netState{sid: {rt: rt}}}
	t.Cleanup(func() { ForgetRuntimeExit(sid.String()) })
	go rt.watchParentOwnedDeath()
	<-rt.DeathCh()
	d.mu.Lock()
	delete(d.nets, sid)
	d.mu.Unlock()
	got, ok := d.RuntimeExit(sid.String())
	if !ok || got != (ExitInfo{Signal: 9}) {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	ForgetRuntimeExit(sid.String())
	if _, ok := d.RuntimeExit(sid.String()); ok {
		t.Fatal("exit retained after Forget")
	}
}

func TestRuntimeExit_RecordsSigkilledChild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -9 $$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rt := &NetnsRuntime{cmd: cmd, deathCh: make(chan struct{})}
	sid := domain.NewSandboxID()
	d := &CHDriver{nets: map[domain.SandboxID]*netState{sid: {rt: rt}}}
	id := sid.String()
	if _, ok := d.RuntimeExit(id); ok {
		t.Fatal("exit reported before death")
	}
	go rt.watchParentOwnedDeath()
	select {
	case <-rt.DeathCh():
	case <-time.After(5 * time.Second):
		t.Fatal("deathCh not closed")
	}
	got, ok := d.RuntimeExit(id)
	if !ok || got != (ExitInfo{Signal: 9}) {
		t.Fatalf("got %+v ok=%v, want Signal 9", got, ok)
	}
	if _, ok := d.RuntimeExit(domain.NewSandboxID().String()); ok {
		t.Fatal("unknown id reported exit")
	}
}

func TestTeardownSandboxNet_ForgetsExit(t *testing.T) {
	sid := domain.NewSandboxID()
	lastExits.Store(sid.String(), ExitInfo{Signal: 9})
	d := &CHDriver{nets: map[domain.SandboxID]*netState{sid: {}}}
	d.teardownSandboxNet(sid)
	if _, ok := d.RuntimeExit(sid.String()); ok {
		t.Fatal("exit retained after teardown")
	}
}
