package cli

import (
	"os"
	"strings"
	"testing"
)

// TestHerdrPluginCreate_DoesNotUseMetadataOnlyCreate pins a live defect: the
// herdr "create a sandbox" action prompted for a project and name, called
// svc.Create, and returned. svc.Create mints a record in state Created with an
// EMPTY Envelope — no image, so the sandbox can never boot. From herdr the
// action produced no VM, no space and no pane, which is indistinguishable from
// the action doing nothing at all, and that is exactly how it was reported.
//
// The fix delegates to the real `sandbox create --image` verb. This test
// asserts the delegation structurally, because the alternative — svc.Create —
// fails silently rather than erroring, so no behavioural test of the happy
// path would catch a regression back to it.
func TestHerdrPluginCreate_DoesNotUseMetadataOnlyCreate(t *testing.T) {
	src, err := os.ReadFile("cmd_herdr_plugin.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "func herdrPluginCreate(")

	if strings.Contains(body, "svc.Create(") {
		t.Error("herdrPluginCreate calls svc.Create — that is metadata-only and yields " +
			"a sandbox with no image that can never boot")
	}
	if !strings.Contains(body, `"--image"`) {
		t.Error("herdrPluginCreate must pass --image so the created sandbox is bootable")
	}
	if !strings.Contains(body, "herdrPluginSpaceCreate(") {
		t.Error("herdrPluginCreate must open a herdr space; otherwise the action " +
			"leaves the user with nothing visible in herdr")
	}
}

// funcBody returns the source text of the function beginning with decl, up to
// the next top-level func declaration.
func funcBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("declaration %q not found", decl)
	}
	rest := src[i+len(decl):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		return rest[:j]
	}
	return rest
}
