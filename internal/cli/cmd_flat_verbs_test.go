package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/service"
)

func TestFlatVerbs_AllRegistered(t *testing.T) {
	want := []string{"create", "ps", "ls", "rm", "start", "stop"}
	for _, name := range want {
		if _, ok := Lookup(name); !ok {
			t.Errorf("flat verb %q is not registered; docs that use it will fail with 'unknown command'", name)
		}
	}
}

func TestFlatVerbs_GroupedSpellingStillRegistered(t *testing.T) {
	if _, ok := Lookup("sandbox"); !ok {
		t.Fatal("the `sandbox` command group was removed; MCP and the herdr plugin call it")
	}
}

func TestFlatVerbs_DelegateToSandboxGroup(t *testing.T) {
	cmd, ok := Lookup("rm")
	if !ok {
		t.Fatal("rm not registered")
	}
	out, _, errBuf := newTestOutput(false)
	err := cmd.Run(t.Context(), []string{}, out)
	if err == nil {
		t.Fatal("`nexus rm` with no args should be a usage error")
	}
	msg := err.Error() + errBuf.String()
	if !strings.Contains(msg, "sandbox rm") {
		t.Errorf("error %q does not come from the sandbox group; flat verb may be reimplementing", msg)
	}
}

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

func TestFlatVerbs_TargetsAreRealSubcommands(t *testing.T) {
	valid := map[string]bool{
		"create": true, "list": true, "rm": true,
		"start": true, "stop": true,
	}
	for _, fv := range flatVerbs {
		if !valid[fv.target] {
			t.Errorf("flat verb %q targets %q, which runSandbox does not dispatch", fv.name, fv.target)
		}
	}
}

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
