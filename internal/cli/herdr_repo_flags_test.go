package cli

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func repoFlagsFor(t *testing.T, input string) ([]string, error) {
	t.Helper()
	return herdrRepoFlags(bufio.NewScanner(strings.NewReader(input)))
}

// TestHerdrRepoFlags_BlankDeclinesToken pins the default posture: no answer
// means no GitHub token, which is the safe choice and, crucially, a choice
// that satisfies D-PD-36 so creation can proceed.
func TestHerdrRepoFlags_BlankDeclinesToken(t *testing.T) {
	got, err := repoFlagsFor(t, "\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "--no-builtin-gh" {
		t.Errorf("blank repo should decline the token, got %v", got)
	}
}

func TestHerdrRepoFlags_ScopesToRepo(t *testing.T) {
	got, err := repoFlagsFor(t, "acme/widgets\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "--repo" || got[1] != "acme/widgets" {
		t.Errorf("got %v, want [--repo acme/widgets]", got)
	}
}

// TestHerdrRepoFlags_RejectsMalformed catches a bad repo before a build is
// spent on it. Without this the error would surface only after the image was
// built, at sandbox-create time.
func TestHerdrRepoFlags_RejectsMalformed(t *testing.T) {
	for _, in := range []string{"widgets\n", "acme/\n", "/widgets\n", "a/b/c\n"} {
		if _, err := repoFlagsFor(t, in); err == nil {
			t.Errorf("repo %q should be rejected as not owner/name", strings.TrimSpace(in))
		}
	}
}

// TestHerdrCreateFlows_SatisfyGitHubGuard pins the defect this fixes: both
// herdr create actions shelled out to `sandbox create` with neither --repo nor
// --no-builtin-gh, so D-PD-36 refused every creation and the actions could
// never succeed — the --file one only after paying for a full image build.
func TestHerdrCreateFlows_SatisfyGitHubGuard(t *testing.T) {
	src, err := os.ReadFile("cmd_herdr_plugin.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	for _, fn := range []string{"func herdrPluginCreate(", "func herdrPluginSpaceCreateFromFile("} {
		b := funcBody(t, body, fn)
		if !strings.Contains(b, "herdrRepoFlags(") {
			t.Errorf("%s does not resolve GitHub-credential flags — D-PD-36 will refuse "+
				"every sandbox it tries to create", strings.TrimSuffix(fn, "("))
		}
	}
}
