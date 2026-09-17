package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHerdrPluginDoctor_ABIMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abi"), []byte(herdrPluginABIVersion+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_ROOT", dir)

	var w strings.Builder
	if err := herdrPluginDoctor(&w); err != nil {
		t.Fatalf("herdrPluginDoctor: %v", err)
	}
	if !strings.Contains(w.String(), "ABI file check: ok") {
		t.Errorf("expected 'ABI file check: ok'; got:\n%s", w.String())
	}
}

func TestHerdrPluginDoctor_ABIMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abi"), []byte("999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_ROOT", dir)

	var w strings.Builder
	if err := herdrPluginDoctor(&w); err != nil {
		t.Fatalf("herdrPluginDoctor: %v", err)
	}
	got := w.String()
	if !strings.Contains(got, "MISMATCH") {
		t.Errorf("expected 'MISMATCH'; got:\n%s", got)
	}
	if !strings.Contains(got, "999") {
		t.Errorf("expected mismatched value '999'; got:\n%s", got)
	}
}

func TestHerdrPluginDoctor_ABIRootUnset(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_ROOT", "")

	var w strings.Builder
	if err := herdrPluginDoctor(&w); err != nil {
		t.Fatalf("herdrPluginDoctor: %v", err)
	}
	if !strings.Contains(w.String(), "HERDR_PLUGIN_ROOT unset") {
		t.Errorf("expected 'HERDR_PLUGIN_ROOT unset'; got:\n%s", w.String())
	}
}

func TestHerdrProcessesCheck_DuplicateServer_Warns(t *testing.T) {
	older := HerdrProc{PID: 100, Start: time.Unix(1000, 0), Kind: HerdrServer, Session: "agents"}
	newer := HerdrProc{PID: 200, Start: time.Unix(2000, 0), Kind: HerdrServer, Session: "agents"}
	lister := func(_ context.Context) ([]HerdrProc, error) {
		return []HerdrProc{older, newer}, nil
	}

	cr := checkHerdrProcesses(context.Background(), lister)

	if cr.OK {
		t.Error("expected WARN (OK=false) for duplicate herdr server")
	}
	if !strings.Contains(cr.Remediation, "100") {
		t.Errorf("remediation must name stale pid 100; got: %s", cr.Remediation)
	}
	if strings.Contains(cr.Remediation, "200") {
		t.Errorf("live pid 200 must not appear in remediation; got: %s", cr.Remediation)
	}
	if !strings.Contains(cr.Remediation, "herdr --session agents server stop") {
		t.Errorf("remediation must include 'herdr --session agents server stop'; got: %s", cr.Remediation)
	}
}

func TestHerdrProcessesCheck_EmptySession_ServerStop(t *testing.T) {
	older := HerdrProc{PID: 100, Start: time.Unix(1000, 0), Kind: HerdrServer, Session: ""}
	newer := HerdrProc{PID: 200, Start: time.Unix(2000, 0), Kind: HerdrServer, Session: ""}
	lister := func(_ context.Context) ([]HerdrProc, error) {
		return []HerdrProc{older, newer}, nil
	}

	cr := checkHerdrProcesses(context.Background(), lister)

	if cr.OK {
		t.Error("expected WARN for duplicate herdr server with empty session")
	}
	if !strings.Contains(cr.Remediation, "herdr server stop") {
		t.Errorf("remediation must contain 'herdr server stop'; got: %s", cr.Remediation)
	}
	if strings.Contains(cr.Remediation, "--session  ") {
		t.Errorf("remediation must not contain '--session  ' (double space); got: %s", cr.Remediation)
	}
}

func TestHerdrProcessesCheck_SingleServer_OK(t *testing.T) {
	lister := func(_ context.Context) ([]HerdrProc, error) {
		return []HerdrProc{{PID: 100, Start: time.Unix(1000, 0), Kind: HerdrServer, Session: "agents"}}, nil
	}
	cr := checkHerdrProcesses(context.Background(), lister)
	if !cr.OK {
		t.Errorf("single herdr server must be OK; got detail: %s", cr.Detail)
	}
}

func TestHerdrProcessesCheck_DuplicateClientAgent_WarnsOlder(t *testing.T) {
	older := HerdrProc{PID: 10, Start: time.Unix(100, 0), Kind: NexusClientAgent}
	newer := HerdrProc{PID: 20, Start: time.Unix(200, 0), Kind: NexusClientAgent}
	lister := func(_ context.Context) ([]HerdrProc, error) {
		return []HerdrProc{older, newer}, nil
	}

	cr := checkHerdrProcesses(context.Background(), lister)

	if cr.OK {
		t.Error("expected WARN (OK=false) for duplicate nexus-client-agent")
	}
	if !strings.Contains(cr.Remediation, "10") {
		t.Errorf("remediation must name stale pid 10; got: %s", cr.Remediation)
	}
	if strings.Contains(cr.Remediation, "20") {
		t.Errorf("live pid 20 must not appear in remediation; got: %s", cr.Remediation)
	}
}

func TestHerdrProcessesCheck_InDoctorJSON(t *testing.T) {
	fixture := []HerdrProc{
		{PID: 10, Start: time.Unix(100, 0), Kind: NexusClientAgent},
		{PID: 20, Start: time.Unix(200, 0), Kind: NexusClientAgent},
	}
	p := probes{
		goos:           "darwin",
		listHerdrProcs: func(_ context.Context) ([]HerdrProc, error) { return fixture, nil },
	}
	checks, _ := runAllChecks(p)
	jsonChecks := toDoctorChecksJSON(checks)

	var found bool
	for _, c := range jsonChecks {
		if c.Name == "herdr_processes" {
			found = true
			if c.OK {
				t.Error("herdr_processes check must be !ok with duplicate agents")
			}
			if c.Remediation == "" {
				t.Error("herdr_processes check must carry non-empty remediation")
			}
			break
		}
	}
	if !found {
		t.Error("herdr_processes check not found in doctor JSON checks")
	}
}
