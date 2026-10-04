package broker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	osexec "os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/sprites/tunnel"
)

var fakeAgent = []byte("fake-agent-binary")

// fakeSprite emulates the guest: a file store, sha256sum, and a relay that
// speaks the guest side of the tunnel.
type fakeSprite struct {
	mu       sync.Mutex
	files    map[string][]byte
	uploads  []string
	reqs     []ExecRequest
	stdin    [][]byte
	sessions chan *tunnel.GuestSession
	relays   int
}

func newFakeSprite() *fakeSprite {
	return &fakeSprite{files: map[string][]byte{}, sessions: make(chan *tunnel.GuestSession, 8)}
}

func (f *fakeSprite) exec(ctx context.Context, _ string, req ExecRequest) (int32, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if req.Argv[0] == GuestAgentPath {
		f.mu.Lock()
		f.relays++
		f.mu.Unlock()
		gs, err := tunnel.Guest(tunnel.Join(req.Stdout.(io.WriteCloser), io.NopCloser(req.Stdin)))
		if err != nil {
			return 1, err
		}
		f.sessions <- gs
		select {
		case <-gs.Closed():
		case <-ctx.Done():
			gs.Close()
		}
		return 1, nil
	}
	script := req.Argv[2]
	switch {
	case strings.Contains(script, "sha256sum"):
		path := req.Argv[4]
		f.mu.Lock()
		b, ok := f.files[path]
		f.mu.Unlock()
		if !ok {
			return 1, nil
		}
		fmt.Fprintf(req.Stdout, "%s  %s\n", hexSHA(b), path)
	case strings.Contains(script, "cat >"):
		b, _ := io.ReadAll(req.Stdin)
		f.mu.Lock()
		f.files[req.Argv[5]] = b
		f.uploads = append(f.uploads, req.Argv[5])
		f.stdin = append(f.stdin, b)
		f.mu.Unlock()
	}
	return 0, nil
}

func TestInstallSkipsWhenShaMatches(t *testing.T) {
	f := newFakeSprite()
	ca := []byte("ca-pem")
	if err := Install(context.Background(), f.exec, "nx-a", fakeAgent, ca); err != nil {
		t.Fatal(err)
	}
	if len(f.uploads) != 2 {
		t.Fatalf("first install uploads = %v", f.uploads)
	}
	f.uploads = nil
	if err := Install(context.Background(), f.exec, "nx-a", fakeAgent, ca); err != nil {
		t.Fatal(err)
	}
	if len(f.uploads) != 0 {
		t.Fatalf("second install re-uploaded: %v", f.uploads)
	}
}

func TestInstallVerifiesShaAfterUpload(t *testing.T) {
	f := newFakeSprite()
	bad := func(ctx context.Context, s string, req ExecRequest) (int32, error) {
		if strings.Contains(req.Argv[2], "cat >") {
			_, _ = io.Copy(io.Discard, req.Stdin) // upload lost
			return 0, nil
		}
		return f.exec(ctx, s, req)
	}
	err := Install(context.Background(), bad, "nx-a", fakeAgent, []byte("ca"))
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func runFixture(t *testing.T) (RunConfig, *fakeSprite) {
	f := newFakeSprite()
	return RunConfig{
		StateDir: t.TempDir(),
		Sprite:   "nx-a",
		Exec:     f.exec,
		Agent:    fakeAgent,
		Creds: CredsConfig{
			SandboxID: domain.NewSandboxID(),
			Secrets: []Secret{
				{Name: "GH_TOKEN", Value: realGH, Hosts: []string{"github.com", "api.github.com"}, GitHubRepo: "acme/widgets"},
				{Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: realClaude, Hosts: []string{"api.anthropic.com"}},
			},
		},
		BackoffMin: time.Millisecond,
		BackoffMax: 5 * time.Millisecond,
	}, f
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestRunLifecycle(t *testing.T) {
	cfg, f := runFixture(t)
	id := cfg.Creds.SandboxID.String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	var gs *tunnel.GuestSession
	select {
	case gs = <-f.sessions:
	case <-time.After(10 * time.Second):
		t.Fatal("relay never started")
	}
	var st State
	waitFor(t, "broker.json", func() bool {
		var err error
		st, err = ReadState(cfg.StateDir, id)
		return err == nil
	})

	// State: real argv, placeholders only.
	if want, _ := ProcessCmdline(st.PID); strings.Join(want, " ") != strings.Join(st.Cmdline, " ") {
		t.Errorf("cmdline %v != process cmdline %v", st.Cmdline, want)
	}
	if st.CAFingerprint == "" || st.GuestCAPath != GuestCAPath {
		t.Errorf("state = %+v", st)
	}
	for _, k := range []string{"HTTPS_PROXY", "NODE_EXTRA_CA_CERTS", "GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if st.GuestEnv[k] == "" {
			t.Errorf("guest env missing %s: %v", k, st.GuestEnv)
		}
	}
	if st.GuestEnv["HTTPS_PROXY"] != "http://127.0.0.1:3128" {
		t.Errorf("HTTPS_PROXY = %q", st.GuestEnv["HTTPS_PROXY"])
	}
	if st.GuestEnv["GH_TOKEN"] == realGH || st.GuestEnv["CLAUDE_CODE_OAUTH_TOKEN"] == realClaude {
		t.Error("real token in guest env")
	}

	// Session close -> fresh relay exec.
	gs.Close()
	select {
	case gs2 := <-f.sessions:
		defer gs2.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("no reconnect after session close")
	}

	// Cancel -> broker.json removed.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if _, err := ReadState(cfg.StateDir, id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("broker.json still present: %v", err)
	}

	// No real token in any exec env, argv or uploaded content; no ghost "secret" uploads.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, real := range []string{realGH, realClaude} {
		for _, r := range f.reqs {
			if strings.Contains(strings.Join(r.Argv, "\x00"), real) {
				t.Errorf("real token in argv %v", r.Argv)
			}
			for k, v := range r.Env {
				if strings.Contains(k+v, real) {
					t.Errorf("real token in env %s", k)
				}
			}
		}
		for _, b := range f.stdin {
			if bytes.Contains(b, []byte(real)) {
				t.Error("real token in uploaded content")
			}
		}
	}
	if f.relays < 2 {
		t.Errorf("relays = %d", f.relays)
	}
}

func TestRunInstallFailureRemovesState(t *testing.T) {
	cfg, _ := runFixture(t)
	cfg.Exec = func(context.Context, string, ExecRequest) (int32, error) { return 0, errors.New("boom") }
	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("want error")
	}
	if _, err := ReadState(cfg.StateDir, cfg.Creds.SandboxID.String()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("state present: %v", err)
	}
}

func TestTrustScriptFailsWithoutCATool(t *testing.T) {
	var stderr bytes.Buffer
	cmd := osexec.Command("/bin/sh", "-c", trustScript, "sh", "/x/ca.pem")
	cmd.Env = []string{"PATH=/nonexistent"}
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil || !strings.Contains(stderr.String(), "update-ca-certificates") {
		t.Fatalf("err=%v stderr=%q; want failure naming the missing CA tools", err, stderr.String())
	}
}

func TestInstallErrorsWhenNoCATool(t *testing.T) {
	f := newFakeSprite()
	noCA := func(ctx context.Context, s string, req ExecRequest) (int32, error) {
		if len(req.Argv) > 2 && strings.Contains(req.Argv[2], "update-ca-certificates") {
			fmt.Fprint(req.Stderr, "neither update-ca-certificates nor update-ca-trust found")
			return 1, nil
		}
		return f.exec(ctx, s, req)
	}
	err := Install(context.Background(), noCA, "nx-a", fakeAgent, []byte("ca"))
	if err == nil || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("err = %v", err)
	}
}
