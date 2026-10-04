package broker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestStateRoundTripAtomic0600(t *testing.T) {
	dir, id := t.TempDir(), domain.NewSandboxID().String()
	want := State{PID: 42, Exe: "/x/nexus", Cmdline: []string{"/x/nexus", "__sprites-broker", id},
		Started: time.Unix(1700000000, 0).UTC(), CAFingerprint: "abcd", Listen: "127.0.0.1:1",
		GuestEnv: map[string]string{"GH_TOKEN": "ph-gh", "HTTPS_PROXY": "http://x:1"}, GuestCAPath: "/etc/ca.pem"}
	if err := WriteState(dir, id, want); err != nil {
		t.Fatal(err)
	}
	p, _ := StatePath(dir, id)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "*.tmp-*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
	got, err := ReadState(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GuestCAPath != want.GuestCAPath || got.GuestEnv["GH_TOKEN"] != "ph-gh" || got.GuestEnv["HTTPS_PROXY"] != "http://x:1" {
		t.Fatalf("guest fields: %+v", got)
	}
	if got.PID != want.PID || got.CAFingerprint != want.CAFingerprint || got.Listen != want.Listen ||
		!got.Started.Equal(want.Started) || strings.Join(got.Cmdline, " ") != strings.Join(want.Cmdline, " ") {
		t.Fatalf("got %+v", got)
	}
	// Overwrite keeps 0600.
	if err := WriteState(dir, id, want); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatal("mode after overwrite")
	}
	if err := RemoveState(dir, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadState(dir, id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	if err := RemoveState(dir, id); err != nil {
		t.Fatalf("double remove: %v", err)
	}
}

func TestStateRejectsTraversalIDs(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "..", "../x", "sb-../../etc", "sb-", "/etc/passwd", "sb-0/../../x", "x"} {
		if err := WriteState(dir, id, State{PID: 1}); err == nil {
			t.Errorf("WriteState(%q) accepted", id)
		}
		if _, err := ReadState(dir, id); err == nil {
			t.Errorf("ReadState(%q) accepted", id)
		}
		if err := RemoveState(dir, id); err == nil {
			t.Errorf("RemoveState(%q) accepted", id)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("state dir touched: %v", entries)
	}
}

func TestStateRejectsNonCanonicalID(t *testing.T) {
	id := domain.NewSandboxID().String()
	alt := "sb-" + strings.ToLower(id[3:])
	if alt == id {
		alt = "sb-" + strings.ToUpper(id[3:])
	}
	if alt == id {
		t.Skip("no case variant")
	}
	if _, err := Dir(t.TempDir(), alt); err == nil {
		// Only meaningful if the parser accepts the variant.
		if p, perr := domain.ParseSandboxID(alt); perr == nil && p.String() != alt {
			t.Fatalf("non-canonical %q accepted", alt)
		}
	}
}
