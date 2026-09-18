package service

import (
	"strings"
	"testing"
)

func TestBuildUserMountScript_PATHOrdering(t *testing.T) {
	extra := []string{"/home/u/.local/bin", "/home/u/x/bin"}
	m := UserMountManifest{ExtraPathDirs: extra}
	script := buildUserMountScript(m)

	const prefix = `export PATH="$PATH:`
	idx := strings.Index(script, prefix)
	if idx < 0 {
		t.Fatalf("PATH line not found in script:\n%s", script)
	}
	line := script[idx : idx+strings.Index(script[idx:], "\n")]
	if !strings.HasPrefix(line, prefix) {
		t.Errorf("PATH line does not start with %q: %q", prefix, line)
	}
	if !strings.HasSuffix(line, `:/home/u/.local/bin:/home/u/x/bin"`) {
		t.Errorf("PATH line does not end with extra dirs: %q", line)
	}
}

func TestBuildUserMountScript_PATHNoExtraDirs(t *testing.T) {
	m := UserMountManifest{}
	script := buildUserMountScript(m)

	suffix := strings.Join(GuestCuratedPATHDirs, ":")
	want := `export PATH="$PATH:` + suffix + `"`
	if !strings.Contains(script, want) {
		t.Errorf("expected %q in script, not found", want)
	}
	const pathPrefix = `export PATH="`
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(line, pathPrefix) {
			continue
		}
		if line != want {
			t.Errorf("PATH line = %q, want %q", line, want)
		}
	}
}
