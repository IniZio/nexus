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
// means no GitHub flags at all — fail-closed (D-PDE-02). A sandbox with no
// GitHub token is the safe choice and satisfies D-PD-36 so creation proceeds.
func TestHerdrRepoFlags_BlankDeclinesToken(t *testing.T) {
	got, err := repoFlagsFor(t, "\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("blank repo should return no flags (fail-closed), got %v", got)
	}
}

func TestHerdrRepoFlags_ScopesToRepo(t *testing.T) {
	got, err := repoFlagsFor(t, "acme/widgets\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect: --repo acme/widgets --secret GH_TOKEN@github.com,api.github.com,uploads.github.com
	if len(got) != 4 || got[0] != "--repo" || got[1] != "acme/widgets" ||
		got[2] != "--secret" || got[3] != "GH_TOKEN@github.com,api.github.com,uploads.github.com" {
		t.Errorf("got %v, want [--repo acme/widgets --secret GH_TOKEN@github.com,api.github.com,uploads.github.com]", got)
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
// herdr create actions shelled out to `sandbox create` without --repo, so
// D-PD-36 refused every creation and the actions could never succeed — the
// --file one only after paying for a full image build.
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
