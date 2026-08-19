package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestHerdrPluginLaunchWiresPerimeterSupervisor is a wiring test: it asserts
// that herdrPluginLaunch actually CALLS the three functions that make
// --agent-egress work, by parsing the source rather than by exercising them.
//
// # Why this test exists in this unusual shape
//
// The feature's essence is "a process starts". Every other test in this package
// is a pure-function test — they check that buildLaunchBootOpts returns the
// right options and that launchCredSourcedArgv builds the right argv — and
// every one of them would stay green if the call to handoffLaunchSupervisor
// were deleted from herdrPluginLaunch. The sandbox would boot, the agent would
// run unauthenticated, and the suite would report success.
//
// That is not a hypothetical. It is exactly what happened before 2026-08-19:
// the previous unit tests asserted that Broker and UseAgentSeed were SET on the
// options struct, and passed for the entire period the feature was
// non-functional, because those options start no proxy. Replacing them with
// better pure-function tests moves that blind spot by one layer; it does not
// remove it.
//
// Exercising the real path instead is not available here: herdrPluginLaunch
// boots a VM through a hard-wired newSandboxService() and a cloud-hypervisor
// driver factory. Until that seam is injectable (TBD-PD-31), an AST check is
// the only guard that turns RED when the wiring is removed.
//
// # What this does and does not prove
//
// It proves the call sites exist. It does NOT prove they run, that their
// arguments are right, or that the perimeter comes up — that evidence is live
// (supervisor log: seeds_complete → real_token_pushed → update_ca_certs_done →
// "mitm: credential swapped") and is not reproducible in a unit test. Treat
// this as a regression tripwire, not as coverage.
func TestHerdrPluginLaunchWiresPerimeterSupervisor(t *testing.T) {
	const src = "cmd_herdr_plugin.go"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var launch *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "herdrPluginLaunch" && fn.Recv == nil {
			launch = fn
			break
		}
	}
	if launch == nil {
		t.Fatalf("herdrPluginLaunch not found in %s — if it was renamed, this test must be "+
			"updated deliberately, not deleted", src)
	}

	called := map[string]bool{}
	ast.Inspect(launch, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			called[id.Name] = true
		}
		return true
	})

	required := []struct {
		fn     string
		reason string
	}{
		{
			"handoffLaunchSupervisor",
			"without it no perimeter process exists: perimeter.Start has exactly one non-test " +
				"caller (Service.startSupervisor), reachable only from Start/Fork/Restore. The " +
				"agent would send a placeholder bearer over an unproxied connection with no CA " +
				"to trust, and fail with 'Not logged in'",
		},
		{
			"launchCredSourcedArgv",
			"without it the guest command never sources /run/nexus3/cred.env, which is the only " +
				"place the guest's placeholder credential exists — the host holds no broker on " +
				"this path and cannot inject it",
		},
		{
			"verifyLaunchPerimeterSeed",
			"without it a supervisor that exhausted its seed retry cap still writes READY, and " +
				"the launch proceeds to fail at the API with an error that names no cause",
		},
	}
	for _, r := range required {
		if !called[r.fn] {
			t.Errorf("herdrPluginLaunch no longer calls %s — %s", r.fn, r.reason)
		}
	}
}
