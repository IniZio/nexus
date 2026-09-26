package livenexus

import (
	"context"
	"testing"
)

// fakeT captures Fatal/Fatalf calls so we can assert refusals without killing
// the outer test.
type fakeT struct {
	fatalMsg string
	failed   bool
}

func (f *fakeT) Helper()                       {}
func (f *fakeT) Fatalf(format string, a ...any) {
	f.failed = true
	f.fatalMsg = format
	panic("fakeT.Fatalf") // stop execution like the real t.Fatalf
}
func (f *fakeT) Logf(format string, a ...any) {}
func (f *fakeT) Cleanup(fn func())             {}

// recoverFatal runs fn, recovering from the fakeT panic.
func recoverFatal(fn func()) (fataled bool) {
	defer func() {
		if r := recover(); r != nil {
			fataled = true
		}
	}()
	fn()
	return false
}

func TestRefusesProdStateRoot(t *testing.T) {
	prod, err := prodStateRoot()
	if err != nil {
		t.Skipf("cannot resolve prod state root: %v", err)
	}
	if err := validateStateRoot(prod); err == nil {
		t.Errorf("validateStateRoot(%q): want error for prod root, got nil", prod)
	}
}

func TestRefusesDefaultHerdrSocket(t *testing.T) {
	sock := prodHerdrSocket()
	if err := validateSocket(sock); err == nil {
		t.Errorf("validateSocket(%q): want error for prod socket, got nil", sock)
	}
}

func TestEnvPinsIsolatedRootAndSocket(t *testing.T) {
	h := &Harness{
		t:           t,
		stateRoot:   "/var/tmp/test-state",
		configHome:  "/var/tmp/test-config",
		dataHome:    "/var/tmp/test-data",
		socketPath:  "/var/tmp/test-config/herdr/sessions/nl-abc/herdr.sock",
		sessionName: "nl-abc",
		nexusBin:    "nexus",
	}
	env := h.Env()

	want := map[string]string{
		"XDG_STATE_HOME":    "/var/tmp/test-state",
		"XDG_DATA_HOME":     "/var/tmp/test-data",
		"HERDR_SOCKET_PATH": "/var/tmp/test-config/herdr/sessions/nl-abc/herdr.sock",
		"TMPDIR":            "/var/tmp",
	}
	got := make(map[string]string)
	for _, e := range env {
		k, v, ok := splitEnv(e)
		if !ok {
			continue
		}
		if _, care := want[k]; care {
			got[k] = v
		}
	}
	for k, wv := range want {
		if gv, ok := got[k]; !ok {
			t.Errorf("Env() missing %s", k)
		} else if gv != wv {
			t.Errorf("Env() %s = %q, want %q", k, gv, wv)
		}
	}
}

func TestCleanupOnlyRegisteredHandles(t *testing.T) {
	var removed []string
	h := &Harness{
		t:    t,
		nexusBin: "nexus",
	}
	h.Track("sb-tracked-1")
	h.Track("sb-tracked-2")

	h.mu.Lock()
	handles := append([]string(nil), h.handles...)
	h.mu.Unlock()

	// Verify only the two tracked handles appear in the cleanup list.
	if len(handles) != 2 {
		t.Fatalf("expected 2 tracked handles, got %d", len(handles))
	}
	for _, want := range []string{"sb-tracked-1", "sb-tracked-2"} {
		found := false
		for _, got := range handles {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tracked handle %q not found in list", want)
		}
	}
	_ = removed
}

func TestRunnerRejectsPrune(t *testing.T) {
	h := &Harness{
		t:          t,
		stateRoot:  "/var/tmp/nexus-live-safe-state",
		nexusBin:   "nexus",
		socketPath: "/var/tmp/nexus-live-safe-config/herdr/sessions/nl-safe/herdr.sock",
	}
	ctx := context.Background()

	pruneArgCases := [][]string{
		{"herdr", "prune"},
		{"prune", "-apply"},
		{"herdr", "prune", "-apply"},
		{"volume", "prune"},
	}
	for _, args := range pruneArgCases {
		_, err := h.Run(ctx, args...)
		if err == nil {
			t.Errorf("Run(%v): expected error refusing prune, got nil", args)
		}
	}
}

func splitEnv(e string) (key, val string, ok bool) {
	for i, c := range e {
		if c == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return "", "", false
}
