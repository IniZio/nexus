package sprites

import (
	"context"
	"testing"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

func TestShellExecIsTTYInCloneDir(t *testing.T) {
	f := &fakeAPI{exitCode: 3}
	d, _ := newTestDriver(t, f)
	code, err := d.Exec(context.Background(), domain.NewSandboxID(), driver.ExecOptions{
		Argv: ShellArgv,
		Cwd:  "/work/repo",
		Pty:  &agentpb.PtyOptions{Term: "xterm", InitialSize: &agentpb.WinSize{Rows: 30, Cols: 100}},
	})
	if err != nil || code != 3 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	r := f.execs[0]
	if !r.TTY || r.Dir != "/work/repo" || r.Rows != 30 || r.Cols != 100 || r.Term != "xterm" {
		t.Errorf("req=%+v", r)
	}
	if r.Argv[0] != "/bin/sh" || r.Argv[len(r.Argv)-1] == "" {
		t.Errorf("argv=%v", r.Argv)
	}
}
