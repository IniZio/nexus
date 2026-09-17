package cli

import (
	"context"
	"strings"
	"testing"
	"time"
)

const linuxPsFixture = `  PID                  STARTED COMMAND
  101 Tue Sep 16 10:00:00 2026 /usr/bin/herdr server
  102 Tue Sep 16 10:01:00 2026 /usr/bin/herdr --session dev server
  103 Tue Sep 16 10:02:00 2026 /usr/bin/herdr remote-client-bridge --session dev
  104 Tue Sep 16 10:03:00 2026 /usr/bin/nexus-client herdr local-agent-startup --session dev
  105 Tue Sep 16 10:04:00 2026 /usr/bin/some-other-process --flag`

const darwinPsFixture = `  PID                  STARTED COMMAND
  201 Wed Sep 16 11:00:00 2026 /usr/local/bin/herdr --session prod server
  202 Wed Sep 16 11:01:00 2026 /usr/local/bin/herdr remote-client-bridge
  203 Wed Sep 16 11:02:00 2026 /usr/local/bin/nexus-client herdr local-agent-startup`

func TestParseHerdrProcsLinux(t *testing.T) {
	procs, err := ParseHerdrProcs(linuxPsFixture, "linux", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(procs) != 4 {
		t.Fatalf("expected 4 procs, got %d: %+v", len(procs), procs)
	}

	check := func(i, wantPID int, wantKind HerdrProcKind, wantSession string, wantYear int) {
		t.Helper()
		p := procs[i]
		if p.PID != wantPID {
			t.Errorf("[%d] PID: got %d, want %d", i, p.PID, wantPID)
		}
		if p.Kind != wantKind {
			t.Errorf("[%d] Kind: got %v, want %v", i, p.Kind, wantKind)
		}
		if p.Session != wantSession {
			t.Errorf("[%d] Session: got %q, want %q", i, p.Session, wantSession)
		}
		if p.Start.Year() != wantYear {
			t.Errorf("[%d] Start year: got %d, want %d", i, p.Start.Year(), wantYear)
		}
		if p.Start.IsZero() {
			t.Errorf("[%d] Start is zero", i)
		}
	}

	check(0, 101, HerdrServer, "", 2026)
	check(1, 102, HerdrServer, "dev", 2026)
	check(2, 103, HerdrRemoteClientBridge, "dev", 2026)
	check(3, 104, NexusClientAgent, "dev", 2026)
}

func TestParseHerdrProcsDarwin(t *testing.T) {
	procs, err := ParseHerdrProcs(darwinPsFixture, "darwin", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(procs) != 3 {
		t.Fatalf("expected 3 procs, got %d: %+v", len(procs), procs)
	}
	if procs[0].Kind != HerdrServer {
		t.Errorf("proc 0: want HerdrServer, got %v", procs[0].Kind)
	}
	if procs[0].Session != "prod" {
		t.Errorf("proc 0 session: got %q, want prod", procs[0].Session)
	}
	if procs[1].Kind != HerdrRemoteClientBridge {
		t.Errorf("proc 1: want HerdrRemoteClientBridge, got %v", procs[1].Kind)
	}
	if procs[2].Kind != NexusClientAgent {
		t.Errorf("proc 2: want NexusClientAgent, got %v", procs[2].Kind)
	}
}

func TestParseHerdrProcsStartTime(t *testing.T) {
	procs, err := ParseHerdrProcs(linuxPsFixture, "linux", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	if !procs[0].Start.Equal(want) {
		t.Errorf("Start: got %v, want %v", procs[0].Start, want)
	}
}

func TestParseHerdrProcsNonHerdrExcluded(t *testing.T) {
	procs, err := ParseHerdrProcs(linuxPsFixture, "linux", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.PID == 105 {
			t.Errorf("non-herdr process PID 105 should be excluded")
		}
	}
}

const threeServerPs = `  PID                  STARTED COMMAND
  556120 Tue Sep 16 10:00:00 2026 /usr/bin/herdr server
  1895133 Tue Sep 16 10:01:00 2026 /usr/bin/herdr server
  1518875 Tue Sep 16 10:02:00 2026 /usr/bin/herdr server`

func TestParseHerdrProcs_ServerSessionFromSocket(t *testing.T) {
	socksByPID := map[int][]string{
		556120:  {"/home/user/.config/herdr/sessions/msbprobe/herdr.sock"},
		1895133: {"/home/user/.config/herdr/herdr.sock"},
		1518875: {"/home/user/.config/herdr/sessions/agents/herdr.sock"},
	}
	finder := func(pid int) []string { return socksByPID[pid] }
	procs, err := ParseHerdrProcs(threeServerPs, "linux", finder)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(procs) != 3 {
		t.Fatalf("want 3 procs, got %d", len(procs))
	}
	want := map[int]string{556120: "msbprobe", 1895133: "", 1518875: "agents"}
	for _, p := range procs {
		if got, ok := want[p.PID]; !ok || p.Session != got {
			t.Errorf("pid %d: session got %q, want %q", p.PID, p.Session, got)
		}
	}
}

func TestParseHerdrProcs_ThreeDistinctSocketServersOK(t *testing.T) {
	socksByPID := map[int][]string{
		556120:  {"/home/user/.config/herdr/sessions/msbprobe/herdr.sock"},
		1895133: {"/home/user/.config/herdr/herdr.sock"},
		1518875: {"/home/user/.config/herdr/sessions/agents/herdr.sock"},
	}
	finder := func(pid int) []string { return socksByPID[pid] }
	procs, err := ParseHerdrProcs(threeServerPs, "linux", finder)
	if err != nil {
		t.Fatal(err)
	}
	lister := func(_ context.Context) ([]HerdrProc, error) { return procs, nil }
	cr := checkHerdrProcesses(context.Background(), lister)
	if !cr.OK {
		t.Errorf("three distinct socket sessions must be OK; detail=%s rem=%s", cr.Detail, cr.Remediation)
	}
}

const dupServerPs = `  PID                  STARTED COMMAND
  100 Tue Sep 16 10:00:00 2026 /usr/bin/herdr server
  200 Tue Sep 16 10:01:00 2026 /usr/bin/herdr server`

func TestParseHerdrProcs_DuplicateServerSameSocket(t *testing.T) {
	socksByPID := map[int][]string{
		100: {"/home/user/.config/herdr/herdr.sock"},
		200: {"/home/user/.config/herdr/herdr.sock"},
	}
	finder := func(pid int) []string { return socksByPID[pid] }
	procs, err := ParseHerdrProcs(dupServerPs, "linux", finder)
	if err != nil {
		t.Fatal(err)
	}
	lister := func(_ context.Context) ([]HerdrProc, error) { return procs, nil }
	cr := checkHerdrProcesses(context.Background(), lister)
	if cr.OK {
		t.Error("two servers sharing the same socket session must WARN")
	}
	if !strings.Contains(cr.Remediation, "100") {
		t.Errorf("stale pid 100 must appear in remediation; got: %s", cr.Remediation)
	}
	if strings.Contains(cr.Remediation, "200") {
		t.Errorf("live pid 200 must not appear in remediation; got: %s", cr.Remediation)
	}
}

func TestParseSSXlpForPID(t *testing.T) {
	ssOut := "Netid State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n" +
		"u_str LISTEN 0 128 /home/user/.config/herdr/sessions/msbprobe/herdr.sock 556120 * 0 users:((\"herdr\",pid=556120,fd=4))\n" +
		"u_str LISTEN 0 128 /home/user/.config/herdr/herdr.sock 1895133 * 0 users:((\"herdr\",pid=1895133,fd=3))\n"

	paths := parseSSXlpForPID(ssOut, 556120)
	if len(paths) != 1 || paths[0] != "/home/user/.config/herdr/sessions/msbprobe/herdr.sock" {
		t.Fatalf("pid 556120: want msbprobe path, got %v", paths)
	}
	paths2 := parseSSXlpForPID(ssOut, 1895133)
	if len(paths2) != 1 || paths2[0] != "/home/user/.config/herdr/herdr.sock" {
		t.Fatalf("pid 1895133: want default path, got %v", paths2)
	}
}

func TestParseLsofFnForSockets(t *testing.T) {
	lsofOut := "p1895133\nn/home/user/.config/herdr/herdr.sock\n"
	paths := parseLsofFnForSockets(lsofOut)
	if len(paths) != 1 || paths[0] != "/home/user/.config/herdr/herdr.sock" {
		t.Fatalf("want default socket path, got %v", paths)
	}
}

func TestParseLsofFnForSockets_Darwin(t *testing.T) {
	lsofOut := "p556120\nn/home/user/.config/herdr/sessions/msbprobe/herdr.sock\n"
	paths := parseLsofFnForSockets(lsofOut)
	if len(paths) != 1 || paths[0] != "/home/user/.config/herdr/sessions/msbprobe/herdr.sock" {
		t.Fatalf("want msbprobe path, got %v", paths)
	}
	s, ok := sessionFromSocketPaths(paths)
	if !ok || s != "msbprobe" {
		t.Fatalf("want session msbprobe, got %q %v", s, ok)
	}
}
