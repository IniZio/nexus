package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// The manual has documented the flat spelling since D-PD-57, and 53 fenced
// invocations across docs/site use it. None of them worked: the binary only
// had `nexus3 sandbox create`, so an operator following the quickstart hit
// "unknown command: create" on its first line.
//
// This asserts every documented flat verb is registered. It is the cheapest
// possible guard against the manual going out of sync with the binary again.
func TestFlatVerbs_AllRegistered(t *testing.T) {
	want := []string{"create", "ps", "ls", "rm", "start", "stop", "pause", "resume"}
	for _, name := range want {
		if _, ok := Lookup(name); !ok {
			t.Errorf("flat verb %q is not registered; docs that use it will fail with 'unknown command'", name)
		}
	}
}

// The grouped spelling must keep working. It is what the MCP tools, the herdr
// plugin and existing scripts call; removing it would break working callers
// for no benefit.
func TestFlatVerbs_GroupedSpellingStillRegistered(t *testing.T) {
	if _, ok := Lookup("sandbox"); !ok {
		t.Fatal("the `sandbox` command group was removed; MCP and the herdr plugin call it")
	}
}

// A flat verb must delegate, not reimplement. If it ever grew its own arg
// parsing the two spellings would drift in flags, error codes and JSON
// envelopes — which is exactly the failure this guards.
func TestFlatVerbs_DelegateToSandboxGroup(t *testing.T) {
	cmd, ok := Lookup("rm")
	if !ok {
		t.Fatal("rm not registered")
	}
	out, _, errBuf := newTestOutput(false)
	err := cmd.Run(t.Context(), []string{}, out)
	if err == nil {
		t.Fatal("`nexus3 rm` with no args should be a usage error")
	}
	// The usage text must come from runSandboxRm, proving delegation.
	msg := err.Error() + errBuf.String()
	if !strings.Contains(msg, "sandbox rm") {
		t.Errorf("error %q does not come from the sandbox group; flat verb may be reimplementing", msg)
	}
}

// ls and ps must be the same command, not two implementations.
func TestFlatVerbs_LsAndPsShareATarget(t *testing.T) {
	var lsTarget, psTarget string
	for _, fv := range flatVerbs {
		switch fv.name {
		case "ls":
			lsTarget = fv.target
		case "ps":
			psTarget = fv.target
		}
	}
	if lsTarget == "" || psTarget == "" {
		t.Fatal("ls or ps missing from flatVerbs")
	}
	if lsTarget != psTarget {
		t.Errorf("ls targets %q but ps targets %q; they must be the same command", lsTarget, psTarget)
	}
}

// Every flat verb must name a real `sandbox` subcommand. A typo here would
// register a command that always fails with "unknown subcommand".
func TestFlatVerbs_TargetsAreRealSubcommands(t *testing.T) {
	valid := map[string]bool{
		"create": true, "list": true, "rm": true,
		"start": true, "stop": true, "pause": true, "resume": true,
	}
	for _, fv := range flatVerbs {
		if !valid[fv.target] {
			t.Errorf("flat verb %q targets %q, which runSandbox does not dispatch", fv.name, fv.target)
		}
	}
}

// `nexus3 ps` used to print only "N sandbox(es)". The rows existed — they went
// into the JSON envelope — but human mode never rendered them, so the primary
// listing command told the operator how many sandboxes there were and nothing
// about any of them.
//
// This drives runSandboxList in human mode, not the renderer in isolation:
// a test of renderTable alone stays green if `ps` stops calling it.
func TestSandboxList_HumanModeRendersRows(t *testing.T) {
	svc := newTestHerdrService(t)
	ctx := t.Context()
	if _, err := svc.Create(ctx, "demo", "listed", service.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	out, stdout, _ := newTestOutput(false)
	if err := runSandboxList(ctx, nil, out, svc); err != nil {
		t.Fatalf("runSandboxList: %v", err)
	}

	got := stdout.String()
	for _, want := range []string{"HANDLE", "STATE", "demo/listed"} {
		if !strings.Contains(got, want) {
			t.Errorf("human output missing %q — an operator cannot see what exists:\n%s", want, got)
		}
	}
}

// JSON mode must NOT gain the table: the envelope is a machine contract and a
// stray table on stdout would corrupt it for any caller parsing the stream.
func TestSandboxList_JSONModeHasNoTable(t *testing.T) {
	svc := newTestHerdrService(t)
	ctx := t.Context()
	if _, err := svc.Create(ctx, "demo", "listed", service.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	out, stdout, _ := newTestOutput(true)
	if err := runSandboxList(ctx, nil, out, svc); err != nil {
		t.Fatalf("runSandboxList: %v", err)
	}

	got := stdout.String()
	if strings.Contains(got, "HANDLE") {
		t.Errorf("JSON mode emitted the human table, corrupting the envelope:\n%s", got)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(got), &envelope); err != nil {
		t.Fatalf("JSON output is not a single valid envelope: %v\n%s", err, got)
	}
}
