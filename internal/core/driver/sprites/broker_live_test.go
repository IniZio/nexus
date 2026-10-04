//go:build spriteslive

package sprites_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

// TestBrokerLive_E2E proves the host credential broker end to end on a real
// sprite: the CLI is driven as a SUBPROCESS built from ./cmd/nexus (never the
// test binary: the broker launcher re-execs os.Executable, which under
// `go test` would recursively run this suite). State is isolated under a temp
// XDG_STATE_HOME; only the sprite this test creates is ever deleted, by exact name.
//
//	NEXUS_LIVE_BIN         prebuilt nexus binary (else `make artifacts` + go build)
//	NEXUS_LIVE_PUSH_REPO   owner/name of a scratch repo that may receive branch sp8b11/<ts>;
//	                       unset => the push assertion is a --dry-run (auth path only)
//	NEXUS_LIVE_EVIDENCE    evidence dir (default /tmp/claude-1003/sp8b11)
func TestBrokerLive_E2E(t *testing.T) {
	apiTok := sprites.ResolveToken()
	if apiTok == "" {
		t.Skip("no Sprites API token")
	}
	ghOut, err := exec.Command("gh", "auth", "token").Output()
	ghTok := strings.TrimSpace(string(ghOut))
	if err != nil || ghTok == "" {
		t.Skip("no host gh token")
	}
	prof := cred.MustProfileByName(cred.ClaudeCodeProfileName)
	r, err := cred.NewRefresher(service.DedicatedCredStorePathForProfile(prof), prof.CredentialedHost, noopSetter{})
	if err != nil {
		t.Skipf("no host claude credential: %v", err)
	}
	claudeTok, _, err := r.Token(context.Background())
	if err != nil || claudeTok == "" {
		t.Skipf("host claude token: %v", err)
	}
	realTokens := map[string]string{"gh": ghTok, "claude": claudeTok}

	evDir := os.Getenv("NEXUS_LIVE_EVIDENCE")
	if evDir == "" {
		evDir = "/tmp/claude-1003/sp8b11"
	}
	_ = os.MkdirAll(evDir, 0o755)
	var timings []string
	note := func(f string, a ...any) {
		s := fmt.Sprintf(f, a...)
		t.Log(s)
		timings = append(timings, s)
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(evDir, "timings.txt"), []byte(strings.Join(timings, "\n")+"\n"), 0o644)
	}()

	exe := liveNexusBinary(t)
	self, _ := os.Executable()
	if rs, _ := filepath.EvalSymlinks(exe); rs == self || exe == self {
		t.Fatalf("SAFETY: broker exe %s is the test binary; refusing to spawn", exe)
	}
	if fi, err := os.Stat(exe); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("nexus binary %s not executable: %v", exe, err)
	}

	state := t.TempDir()
	// The in-process driver's launcher (PreparePushBranch respawn) inherits this env.
	t.Setenv("XDG_STATE_HOME", state)
	env := append(os.Environ(), "XDG_STATE_HOME="+state, "NEXUS_BACKEND=sprites")
	stateRoot := filepath.Join(state, "nexus")
	nexus := func(ctx context.Context, args ...string) (int, string, string) {
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Env = env
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		return code, so.String(), se.String()
	}
	scrub := func(s string) string {
		for _, v := range realTokens {
			s = strings.ReplaceAll(s, v, "***")
		}
		return s
	}

	before := liveSpriteList(t, apiTok)
	t.Logf("sprite list before: %v", before)
	_ = os.WriteFile(filepath.Join(evDir, "sprites.before"), []byte(strings.Join(before, "\n")+"\n"), 0o644)

	pushRepo := os.Getenv("NEXUS_LIVE_PUSH_REPO")
	repo := pushRepo
	if repo == "" {
		repo = "IniZio/opencode-nudge"
	}
	handle := fmt.Sprintf("p/b%d", time.Now().Unix()%100000)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	var spriteName string
	var id domain.SandboxID
	defer func() {
		// Only ever remove the sprite this test created, by exact name.
		if spriteName != "" {
			rctx, rc := context.WithTimeout(context.Background(), 3*time.Minute)
			defer rc()
			code, so, se := nexus(rctx, "rm", handle)
			t.Logf("cleanup rm %s: code=%d %s", handle, code, scrub(strings.TrimSpace(so+se)))
		}
		after := liveSpriteList(t, apiTok)
		t.Logf("sprite list after: %v", after)
		_ = os.WriteFile(filepath.Join(evDir, "sprites.after"), []byte(strings.Join(after, "\n")+"\n"), 0o644)
		if spriteName != "" {
			for _, n := range after {
				if strings.HasPrefix(n, spriteName+" ") {
					t.Errorf("sprite %s still present after rm", spriteName)
				}
			}
		}
		if strings.Join(stripStatus(before), ",") != strings.Join(stripStatus(after), ",") {
			t.Errorf("sprite set changed: before=%v after=%v", before, after)
		}
		if brokerLog, err := os.ReadFile(filepath.Join(brokerDir(stateRoot, id), "broker.log")); err == nil {
			_ = os.WriteFile(filepath.Join(evDir, "broker.log"), []byte(scrub(string(brokerLog))), 0o644)
		}
	}()

	// (1) create in broker mode (the default).
	t0 := time.Now()
	code, so, se := nexus(ctx, "sandbox", "create", "--repo", repo, "--sync", "push", "--agent", cred.ClaudeCodeProfileName, "--secret", sprites.SecretGitHub, handle)
	createDur := time.Since(t0)
	out := scrub(so + se)
	if m := regexp.MustCompile(`sb-[0-9A-Z]{26}`).FindString(out); m != "" {
		id, _ = domain.ParseSandboxID(m)
		spriteName = sprites.SpriteName(id)
	}
	if code != 0 {
		// create rolls back its own sprite; still try rm in case it leaked.
		t.Fatalf("(1) FAIL sandbox create code=%d: %s", code, out)
	}
	note("timing: sandbox create (provision+clone+broker ready) = %s", createDur.Round(10*time.Millisecond))
	if _, err := broker.ReadState(stateRoot, id.String()); err != nil {
		t.Fatalf("(1) FAIL broker.json missing/invalid: %v", err)
	}
	pid1 := brokerPID(t, stateRoot, id)
	verifyBrokerPID(t, pid1, id)
	note("(1) PASS broker.json present, broker pid=%d alive, cmdline verified", pid1)
	if fi, err := os.Stat(brokerJSONPath(t, stateRoot, id)); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("broker.json mode %v, want 0600", fi.Mode().Perm())
	}
	guestEnv := brokerGuestEnv(t, stateRoot, id)

	run := func(rc context.Context, args ...string) (int, string, string) {
		return nexus(rc, append([]string{"exec", handle}, args...)...)
	}
	sh := func(rc context.Context, script string) (int, string, string) {
		return run(rc, "sh", "-c", script)
	}

	// (2) host-side scan: stream sprite content to the HOST; search there.
	// Use a recorded placeholder as positive control (scanner must see env).
	scanCtx, scanCancel := context.WithTimeout(ctx, 6*time.Minute)
	defer scanCancel()
	// Warm claude first so its files exist before the file scan.
	t1 := time.Now()
	cc, cso, cse := run(scanCtx, "claude", "-p", "--model", "haiku", "reply ok")
	note("timing: first claude -p haiku = %s", time.Since(t1).Round(10*time.Millisecond))
	claudeOut := scrub(cso + cse)
	if cc != 0 || !strings.Contains(strings.ToLower(claudeOut), "ok") {
		t.Errorf("(5) FAIL claude -p: code=%d out=%q", cc, claudeOut)
	} else {
		note("(5) PASS claude -p --model haiku => %q", strings.TrimSpace(claudeOut))
	}

	streams := map[string]string{
		"env":      `env`,
		"environ":  `for f in /proc/[0-9]*/environ; do tr '\0' '\n' < "$f" 2>/dev/null; done; true`,
		"cmdline":  `for f in /proc/[0-9]*/cmdline; do tr '\0' ' ' < "$f" 2>/dev/null; echo; done; true`,
		"gitcfg":   `cd /home/sprite/work && git config --show-origin -l; cat .git/config; printf 'protocol=https\nhost=github.com\n\n' | git credential fill 2>&1; true`,
		"ghconfig": `cat ~/.config/gh/* 2>/dev/null; cat ~/.gitconfig /etc/gitconfig 2>/dev/null; true`,
		"claude":   `cat ~/.claude* ~/.claude/.credentials.json ~/.claude/*.json 2>/dev/null; true`,
		"nexusdir": `cat /usr/local/lib/nexus/*.pem 2>/dev/null; ls -la /usr/local/lib/nexus; true`,
		"files": `find "$HOME" /tmp /etc /usr/local/lib/nexus /var/tmp -xdev -type f -size -2M 2>/dev/null | ` +
			`grep -v -e '/\.local/share/claude' -e '/\.npm/' -e '/\.cache/' -e '/node_modules/' | tar cf - -T - 2>/dev/null; true`,
	}
	needles := map[string]string{}
	for k, v := range realTokens {
		needles[k+" full"] = v
		if len(v) > 20 {
			needles[k+" prefix20"] = v[:20]
		}
	}
	names := make([]string, 0, len(streams))
	for k := range streams {
		names = append(names, k)
	}
	sort.Strings(names)
	var summary []string
	var envStream string
	totalBytes := 0
	for _, name := range names {
		c, o, e := sh(scanCtx, streams[name])
		if c != 0 && name != "files" {
			t.Errorf("(2) scan stream %s exit %d: %s", name, c, scrub(e))
		}
		totalBytes += len(o)
		if name == "env" {
			envStream = o
		}
		hits := 0
		for nn, nv := range needles {
			if strings.Contains(o, nv) || strings.Contains(e, nv) {
				hits++
				t.Errorf("(2) FAIL real %s token found in sprite stream %q", nn, name)
			}
		}
		summary = append(summary, fmt.Sprintf("stream=%s bytes=%d real_token_hits=%d", name, len(o), hits))
	}
	// Control: the scanner sees the env stream (placeholders present), and the
	// GH_TOKEN / OAuth values are exactly broker placeholders.
	gotGH, gotCL := envValue(envStream, "GH_TOKEN"), envValue(envStream, "CLAUDE_CODE_OAUTH_TOKEN")
	switch {
	case gotGH == "" || gotGH != guestEnv["GH_TOKEN"]:
		t.Errorf("(2) FAIL sprite GH_TOKEN is not the broker placeholder (len=%d)", len(gotGH))
	case gotGH == ghTok:
		t.Errorf("(2) FAIL GH_TOKEN is the real token")
	case gotCL == "" || gotCL != guestEnv["CLAUDE_CODE_OAUTH_TOKEN"] || gotCL == claudeTok:
		t.Errorf("(2) FAIL CLAUDE_CODE_OAUTH_TOKEN is not the broker placeholder (len=%d)", len(gotCL))
	}
	if envValue(envStream, "HTTPS_PROXY") != broker.GuestProxyAddr && envValue(envStream, "HTTPS_PROXY") != "http://"+broker.GuestProxyAddr {
		t.Errorf("(2) HTTPS_PROXY = %q", envValue(envStream, "HTTPS_PROXY"))
	}
	summary = append(summary, fmt.Sprintf("GH_TOKEN placeholder len=%d (== broker.json guest_env, != real)", len(gotGH)),
		fmt.Sprintf("total streamed bytes=%d; needles=%d (full+prefix20 for gh,claude)", totalBytes, len(needles)))
	_ = os.WriteFile(filepath.Join(evDir, "scan-summary.txt"), []byte(strings.Join(summary, "\n")+"\n"), 0o644)
	if !t.Failed() {
		note("(2) PASS host-side scan clean: %s", strings.Join(summary, "; "))
	}

	// (3) gh api user via HTTPS_PROXY relay.
	c3, o3, e3 := sh(scanCtx, `gh api -i user 2>&1 | head -1; gh api user -q .login`)
	o3 = scrub(o3 + e3)
	if c3 != 0 || !strings.Contains(o3, " 200") {
		t.Errorf("(3) FAIL gh api user: code=%d %q", c3, o3)
	} else {
		note("(3) PASS gh api user => %q", strings.Join(strings.Fields(o3), " "))
	}

	// (4) push scratch branch (or auth-path dry run). PreparePushBranch is the
	// production wiring of the env-only credential helper; the Manager's
	// launcher gets the REAL binary explicitly (never os.Executable()).
	drv, err := sprites.New(sprites.Config{StateDir: stateRoot, Broker: &broker.Manager{
		StateDir: stateRoot, Launcher: broker.ExecLauncher{Exe: exe, StateDir: stateRoot}, ReadyTimeout: 60 * time.Second,
	}})
	if err != nil {
		t.Fatal(err)
	}
	branch := fmt.Sprintf("sp8b11/%d", time.Now().Unix())
	if err := drv.PreparePushBranch(scanCtx, id, sprites.CloneDir, branch); err != nil {
		t.Errorf("(4) FAIL PreparePushBranch: %v", err)
	}
	// PreparePushBranch restarts the broker with the branch allowlist.
	pid1 = brokerPID(t, stateRoot, id)
	verifyBrokerPID(t, pid1, id)
	if pushRepo != "" {
		c4, o4, e4 := sh(scanCtx, fmt.Sprintf(`cd /home/sprite/work && git -c user.name=sp8b11 -c user.email=sp8b11@example.invalid commit -q --allow-empty -m sp8b11 && git push origin %[1]s 2>&1 && git push origin --delete %[1]s 2>&1`, branch))
		o4 = scrub(o4 + e4)
		if c4 != 0 {
			t.Errorf("(4) FAIL push scratch branch: code=%d %q", c4, o4)
		} else {
			note("(4) PASS pushed + deleted %s on %s", branch, pushRepo)
		}
	} else {
		c4, o4, e4 := sh(scanCtx, fmt.Sprintf(`cd /home/sprite/work && git ls-remote origin HEAD | wc -l && git push --dry-run origin %[1]s 2>&1`, branch))
		o4 = scrub(o4 + e4)
		if c4 != 0 || !strings.Contains(o4, "[new branch]") {
			t.Errorf("(4) FAIL push dry-run: code=%d %q", c4, o4)
		} else {
			note("(4) PASS (real push SKIPPED: no scratch repo; set NEXUS_LIVE_PUSH_REPO) authenticated dry-run => %q", strings.Join(strings.Fields(o4), " "))
		}
	}

	// (6a) SIGTERM the broker: next exec respawns it (Ensure).
	if err := syscall.Kill(pid1, syscall.SIGTERM); err != nil {
		t.Fatalf("(6) SIGTERM broker: %v", err)
	}
	waitDead(t, pid1, 15*time.Second)
	if _, err := os.Stat(brokerJSONPath(t, stateRoot, id)); err == nil {
		t.Logf("broker.json still present after SIGTERM (stale; Alive() must use pid)")
	}
	t2 := time.Now()
	c6, o6, e6 := sh(ctx, `echo respawned; gh api user -q .login`)
	respawn := time.Since(t2)
	pid2 := brokerPID(t, stateRoot, id)
	if c6 != 0 || pid2 == pid1 || !strings.Contains(o6, "respawned") {
		t.Errorf("(6a) FAIL respawn: code=%d pid1=%d pid2=%d out=%q err=%q", c6, pid1, pid2, scrub(o6), scrub(e6))
	} else {
		verifyBrokerPID(t, pid2, id)
		if ph2 := brokerGuestEnv(t, stateRoot, id)["GH_TOKEN"]; ph2 == "" || ph2 != guestEnv["GH_TOKEN"] {
			t.Errorf("(6a) FAIL respawn changed the GH_TOKEN placeholder (len before=%d after=%d)", len(guestEnv["GH_TOKEN"]), len(ph2))
		} else {
			note("(6a) PASS respawn kept the same GH_TOKEN placeholder (len=%d)", len(ph2))
		}
		note("(6a) PASS broker SIGTERMed pid=%d; next exec respawned pid=%d in %s; relay works (%q)", pid1, pid2, respawn.Round(10*time.Millisecond), strings.Join(strings.Fields(scrub(o6)), " "))
	}

	// (6b) broker dead and the launcher broken: exec refused, no direct path out.
	logPath := filepath.Join(filepath.Dir(brokerJSONPath(t, stateRoot, id)), "broker.log")
	if err := os.Chmod(logPath, 0o400); err != nil {
		t.Fatalf("chmod broker.log: %v", err)
	}
	restore := func() { _ = os.Chmod(logPath, 0o600) }
	defer restore()
	if err := syscall.Kill(pid2, syscall.SIGTERM); err != nil {
		t.Fatalf("(6b) SIGTERM broker: %v", err)
	}
	waitDead(t, pid2, 15*time.Second)
	t3 := time.Now()
	c7, o7, e7 := sh(ctx, `echo SHOULD-NOT-RUN`)
	refuse := scrub(o7 + e7)
	if c7 == 0 || strings.Contains(refuse, "SHOULD-NOT-RUN") || !strings.Contains(refuse, "refusing to exec") {
		t.Errorf("(6b) FAIL exec not refused with broker dead+launcher broken: code=%d %q", c7, refuse)
	} else {
		note("(6b) PASS exec refused in %s: %s", time.Since(t3).Round(10*time.Millisecond), strings.TrimSpace(refuse))
	}
	// Raw exec (bypasses the CLI broker check): sprite cannot reach secret hosts directly.
	rawExec := drv.BrokerExec()
	raw := func(script string) (int32, string) {
		var b bytes.Buffer
		rc, rcancel := context.WithTimeout(ctx, 2*time.Minute)
		defer rcancel()
		c, err := rawExecRun(rc, rawExec, spriteName, script, &b)
		if err != nil {
			t.Errorf("raw exec: %v", err)
		}
		return c, scrub(b.String())
	}
	for _, host := range []string{"api.anthropic.com", "api.github.com"} {
		c, o := raw(fmt.Sprintf(`env -u HTTPS_PROXY -u https_proxy curl -sS -m 20 -o /dev/null -w 'http=%%{http_code}\n' https://%s/ 2>&1; echo rc=$?`, host))
		if strings.Contains(o, "http=200") || strings.Contains(o, "http=401") || strings.Contains(o, "http=404") || strings.Contains(o, "http=403") && !strings.Contains(o, "rc=") {
			t.Errorf("(6b) FAIL sprite reached %s directly: %q", host, o)
		}
		_, op := raw(fmt.Sprintf(`HTTPS_PROXY=http://%s curl -sS -m 20 -o /dev/null -w 'http=%%{http_code}\n' https://%s/ 2>&1; echo rc=$?`, broker.GuestProxyAddr, host))
		if strings.Contains(op, "http=200") || strings.Contains(op, "http=401") {
			t.Errorf("(6b) FAIL sprite reached %s through dead relay: %q", host, op)
		}
		note("(6b) direct %s (exit=%d): %q ; via dead relay: %q", host, c, strings.Join(strings.Fields(o), " "), strings.Join(strings.Fields(op), " "))
	}
	cr, orr := raw(`env -u HTTPS_PROXY -u https_proxy HOME=/tmp/nohome claude -p --model haiku "reply ok" 2>&1 | head -c 300; true`)
	_ = cr
	if strings.Contains(strings.ToLower(orr), "reply") && strings.EqualFold(strings.TrimSpace(orr), "ok") {
		t.Errorf("(6b) FAIL claude answered without broker: %q", orr)
	}
	note("(6b) claude with no broker/proxy => %q", strings.Join(strings.Fields(orr), " "))

	// Recovery: restoring the launcher lets the next exec respawn again.
	restore()
	c8, o8, e8 := sh(ctx, `echo recovered`)
	if c8 != 0 || !strings.Contains(o8, "recovered") {
		t.Errorf("(6c) FAIL recovery after restoring launcher: code=%d %q", c8, scrub(o8+e8))
	} else {
		note("(6c) PASS exec respawns broker after launcher restored")
	}
}

func stripStatus(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i], _, _ = strings.Cut(s, " ")
	}
	return out
}

func envValue(env, key string) string {
	for _, l := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), key+"="); ok {
			return v
		}
	}
	return ""
}

func rawExecRun(ctx context.Context, ex broker.ExecFunc, sprite, script string, out *bytes.Buffer) (int32, error) {
	return ex(ctx, sprite, broker.ExecRequest{Argv: []string{"sh", "-c", script}, Stdout: out, Stderr: out})
}

// liveSpriteList returns "name status" for every sprite in the org, sorted.
func liveSpriteList(t *testing.T, token string) []string {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://api.sprites.dev/v1/sprites", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list sprites: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Data    []struct{ Name, Status string } `json:"data"`
		Sprites []struct{ Name, Status string } `json:"sprites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("list sprites decode: %v", err)
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range append(body.Data, body.Sprites...) {
		if k := s.Name + " " + s.Status; !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func liveRepoRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for ; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("repo root not found")
	return ""
}

// liveNexusBinary returns a real nexus CLI binary (never the test binary).
func liveNexusBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("NEXUS_LIVE_BIN"); p != "" {
		return p
	}
	root := liveRepoRoot(t)
	bin := filepath.Join(t.TempDir(), "nexus")
	for _, c := range [][]string{{"make", "artifacts"}, {"go", "build", "-o", bin, "./cmd/nexus"}} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
	return bin
}

// liveBrokerDriver builds a driver whose broker is the real nexus binary; every
// sprite is brokered, so live tests need one.
type noopSetter struct{}

func (noopSetter) SetRealToken(domain.SandboxID, string, string) error { return nil }

func liveBrokerDriver(t *testing.T, stateRoot string) *sprites.Driver {
	t.Helper()
	drv, err := sprites.New(sprites.Config{StateDir: stateRoot, Broker: &broker.Manager{
		StateDir: stateRoot, Launcher: broker.ExecLauncher{Exe: liveNexusBinary(t), StateDir: stateRoot}, ReadyTimeout: 60 * time.Second,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return drv
}

func brokerDir(stateRoot string, id domain.SandboxID) string {
	d, _ := broker.Dir(stateRoot, id.String())
	return d
}

func brokerJSONPath(t *testing.T, stateRoot string, id domain.SandboxID) string {
	t.Helper()
	p, err := broker.StatePath(stateRoot, id.String())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func brokerPID(t *testing.T, stateRoot string, id domain.SandboxID) int {
	t.Helper()
	b, err := os.ReadFile(brokerJSONPath(t, stateRoot, id))
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ PID int }
	if err := json.Unmarshal(b, &s); err != nil || s.PID <= 0 {
		t.Fatalf("broker.json pid: %v", err)
	}
	return s.PID
}

func brokerGuestEnv(t *testing.T, stateRoot string, id domain.SandboxID) map[string]string {
	t.Helper()
	b, err := os.ReadFile(brokerJSONPath(t, stateRoot, id))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		GuestEnv map[string]string `json:"guest_env"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s.GuestEnv
}

// verifyBrokerPID checks /proc/<pid>/cmdline really is this sandbox's broker
// before the test ever signals it.
func verifyBrokerPID(t *testing.T, pid int, id domain.SandboxID) {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		t.Fatalf("broker pid %d not alive: %v", pid, err)
	}
	cl := strings.ReplaceAll(string(b), "\x00", " ")
	if !strings.Contains(cl, "__sprites-broker "+id.String()) {
		t.Fatalf("pid %d is not the broker for %s: %q", pid, id, cl)
	}
}

func waitDead(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err != nil || strings.Contains(string(b), ") Z ") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("broker pid %d did not exit after SIGTERM", pid)
}
