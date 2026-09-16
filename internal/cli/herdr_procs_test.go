package cli

import (
	"testing"
	"time"
)

const linuxPsFixture = `  PID                  STARTED COMMAND
  101 Tue Sep 16 10:00:00 2026 /usr/bin/herdr server
  102 Tue Sep 16 10:01:00 2026 /usr/bin/herdr --session dev server
  103 Tue Sep 16 10:02:00 2026 /usr/bin/herdr remote-client-bridge --session dev
  104 Tue Sep 16 10:03:00 2026 /usr/bin/nexus3-client herdr local-agent-startup --session dev
  105 Tue Sep 16 10:04:00 2026 /usr/bin/some-other-process --flag`

const darwinPsFixture = `  PID                  STARTED COMMAND
  201 Wed Sep 16 11:00:00 2026 /usr/local/bin/herdr --session prod server
  202 Wed Sep 16 11:01:00 2026 /usr/local/bin/herdr remote-client-bridge
  203 Wed Sep 16 11:02:00 2026 /usr/local/bin/nexus3-client herdr local-agent-startup`

func TestParseHerdrProcsLinux(t *testing.T) {
	procs, err := ParseHerdrProcs(linuxPsFixture, "linux")
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
	check(3, 104, Nexus3ClientAgent, "dev", 2026)
}

func TestParseHerdrProcsDarwin(t *testing.T) {
	procs, err := ParseHerdrProcs(darwinPsFixture, "darwin")
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
	if procs[2].Kind != Nexus3ClientAgent {
		t.Errorf("proc 2: want Nexus3ClientAgent, got %v", procs[2].Kind)
	}
}

func TestParseHerdrProcsStartTime(t *testing.T) {
	procs, err := ParseHerdrProcs(linuxPsFixture, "linux")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	if !procs[0].Start.Equal(want) {
		t.Errorf("Start: got %v, want %v", procs[0].Start, want)
	}
}

func TestParseHerdrProcsNonHerdrExcluded(t *testing.T) {
	procs, err := ParseHerdrProcs(linuxPsFixture, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.PID == 105 {
			t.Errorf("non-herdr process PID 105 should be excluded")
		}
	}
}
