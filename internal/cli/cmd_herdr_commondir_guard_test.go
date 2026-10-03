package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/supervisor"
)

func TestHerdrCommonDirHostPathWarning(t *testing.T) {
	cd := t.TempDir()
	checkout := t.TempDir()
	cfg := "[core]\n\thooksPath = /home/x/repo/.githooks\n\texcludesFile = ~/.gitignore\n\tfoo = /abs\n" +
		"[includeIf \"gitdir:/x/\"]\n\tpath = /home/x/inc\n"
	if err := os.WriteFile(filepath.Join(cd, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := herdrCommonDirHostPathWarning(cd, checkout)
	for _, w := range []string{"core.hookspath=/home/x/repo/.githooks", "core.excludesfile=~/.gitignore", "includeif.gitdir:/x/.path=/home/x/inc"} {
		if !strings.Contains(msg, w) {
			t.Errorf("warning %q missing %q", msg, w)
		}
	}
	if strings.Contains(msg, "foo") || strings.Contains(msg, "\n") {
		t.Errorf("warning must be one line and skip non-path keys: %q", msg)
	}
	if err := os.WriteFile(filepath.Join(cd, "config"), []byte("[core]\n\thooksPath = .githooks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := herdrCommonDirHostPathWarning(cd, checkout); m != "" {
		t.Errorf("relative hooksPath warned: %q", m)
	}
}

func TestGitCommonMountFlagRoundTrip(t *testing.T) {
	dir := t.TempDir()
	lm, err := parseMountLive(dir + ":" + dir + ":gitcommon")
	if err != nil || !lm.GitCommon || lm.ReadOnly {
		t.Fatalf("parseMountLive: %+v, %v", lm, err)
	}
	back, err := supervisor.ParseLiveMountSpec(supervisor.EncodeLiveMount(lm))
	if err != nil || !back.GitCommon {
		t.Fatalf("supervisor round trip: %+v, %v", back, err)
	}
	cmdline := workspaceMountCmdline(liveMountsToGuestMounts([]domain.LiveMount{lm}))
	if !strings.HasSuffix(cmdline, ":gitcommon") {
		t.Fatalf("cmdline missing marker: %q", cmdline)
	}
}
