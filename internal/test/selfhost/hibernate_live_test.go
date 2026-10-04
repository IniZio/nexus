//go:build integration

package selfhost

// hibernate_live_test.go — HB-15: live cloud-hypervisor hibernate/resume e2e.
//
// Drives a freshly built nexus CLI as a subprocess with an ISOLATED state root
// (XDG_STATE_HOME under /var/tmp), so it never touches prod state, port 7777 or
// prod sandboxes. The test binary is never re-executed. It only signals PIDs
// whose /proc cmdline names the sandbox id it created.
//
//	flock /tmp/claude-1003/h0-make.lock make artifacts
//	go build -o /var/tmp/hb15/nexus ./cmd/nexus
//	TMPDIR=/var/tmp flock /tmp/claude-1003/h0-make.lock make test \
//	    GOTEST_PKGS=./internal/test/selfhost/ \
//	    GOTEST_ARGS='-tags integration -run TestHibernate -v -timeout 40m'
//
// HB15_NEXUS_BIN overrides the binary path (default /var/tmp/hb15/nexus).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type hbCLI struct {
	t    *testing.T
	bin  string
	root string // XDG_STATE_HOME
	ws   string
	env  []string
}

type hbResult struct {
	stdout, stderr string
	code           int
	env            struct {
		Kind  string          `json:"kind"`
		Data  json.RawMessage `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
}

func (r *hbResult) data(v any) {
	if err := json.Unmarshal(r.env.Data, v); err != nil {
		panic(fmt.Sprintf("decode data %s: %v", r.env.Data, err))
	}
}

func newHBCLI(t *testing.T) *hbCLI {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm")
	}
	bin := os.Getenv("HB15_NEXUS_BIN")
	if bin == "" {
		bin = "/var/tmp/hb15/nexus"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("nexus binary %s missing (go build -o %s ./cmd/nexus)", bin, bin)
	}
	base, err := os.MkdirTemp("/var/tmp", "hb15-live-")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "state")
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(ws, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	cf := "FROM alpine:3.20\nRUN apk add --no-cache python3 && mkdir -p /www && echo hello-hb15 > /www/index.html\n"
	if err := os.WriteFile(filepath.Join(ws, ".nexus", "Containerfile"), []byte(cf), 0o644); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "NEXUS_") || strings.HasPrefix(e, "XDG_STATE_HOME=") || strings.HasPrefix(e, "TMPDIR=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "XDG_STATE_HOME="+root, "TMPDIR=/var/tmp")
	t.Logf("isolated state root: %s", root)
	return &hbCLI{t: t, bin: bin, root: root, ws: ws, env: env}
}

func (c *hbCLI) run(timeout time.Duration, args ...string) *hbResult {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = c.ws
	cmd.Env = c.env
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	r := &hbResult{stdout: so.String(), stderr: se.String()}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			r.code = ee.ExitCode()
		} else {
			r.code = -1
		}
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "{") {
			_ = json.Unmarshal([]byte(lines[i]), &r.env)
			break
		}
	}
	c.t.Logf("$ nexus %s -> rc=%d kind=%s %s", strings.Join(args, " "), r.code, r.env.Kind, strings.TrimSpace(string(r.env.Data)))
	if r.env.Error.Code != "" {
		c.t.Logf("  error %s: %s", r.env.Error.Code, r.env.Error.Message)
	}
	return r
}

// guest runs script via sh -c in the sandbox and returns trimmed stdout.
func (c *hbCLI) guest(ref, script string) string {
	c.t.Helper()
	r := c.run(60*time.Second, "exec", ref, "--", "sh", "-c", script)
	if r.code != 0 {
		c.t.Fatalf("guest exec %q: rc=%d stdout=%q stderr=%q", script, r.code, r.stdout, r.stderr)
	}
	return strings.TrimSpace(r.stdout)
}

type hbProc struct {
	pid int
	cmd string
}

// procsWithID lists processes whose cmdline contains id (VMM api-socket path,
// supervisor --sandbox-ref, virtiofsd, ...).
func procsWithID(id string) []hbProc {
	var out []hbProc
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		cmd := strings.ReplaceAll(string(raw), "\x00", " ")
		if strings.Contains(cmd, id) {
			out = append(out, hbProc{pid, cmd})
		}
	}
	return out
}

func findProc(id, needle string) (hbProc, bool) {
	for _, p := range procsWithID(id) {
		match := strings.Contains(p.cmd, needle)
		if needle == "cloud-hypervisor" { // argv0 only: the supervisor's --ch-bin also contains it
			match = strings.HasSuffix(strings.Fields(p.cmd)[0], "/cloud-hypervisor")
		}
		if match {
			return p, true
		}
	}
	return hbProc{}, false
}

func vmRSSKiB(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmRSS:") {
			f := strings.Fields(l)
			n, _ := strconv.ParseInt(f[1], 10, 64)
			return n
		}
	}
	return 0
}

func killIDProcs(t *testing.T, id string) {
	for _, p := range procsWithID(id) {
		// Re-verify cmdline right before signalling.
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.pid))
		if err != nil || !strings.Contains(string(raw), id) {
			continue
		}
		t.Logf("killing leftover pid %d: %.120s", p.pid, p.cmd)
		_ = syscall.Kill(p.pid, syscall.SIGKILL)
	}
}

func median(xs []int64) int64 {
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// hostPortFor waits for a live forward of guest port to appear in the
// isolated portfwd state and returns the host port.
func (c *hbCLI) hostPortFor(id string, guestPort uint16, wait time.Duration) uint16 {
	c.t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(c.root, "nexus", "portfwd", "forwards.state"))
		if err == nil {
			var st struct {
				Forwards []struct {
					Port     uint16 `json:"port"`
					HostPort uint16 `json:"host_port"`
					Sandbox  string `json:"sandbox"`
					Status   string `json:"status"`
				} `json:"forwards"`
			}
			if json.Unmarshal(b, &st) == nil {
				for _, f := range st.Forwards {
					if f.Sandbox == id && f.Port == guestPort && f.Status == "live" && f.HostPort != 0 {
						return f.HostPort
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	c.t.Fatalf("no live forward for %s guest port %d within %s", id, guestPort, wait)
	return 0
}

func httpGet(port uint16, wait time.Duration) (string, time.Duration, error) {
	start := time.Now()
	var last error
	for time.Since(start) < wait {
		cl := http.Client{Timeout: 2 * time.Second}
		resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return strings.TrimSpace(string(b)), time.Since(start), nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return "", time.Since(start), last
}

type hbHib struct {
	ID                  string `json:"id"`
	State               string `json:"state"`
	Already             bool   `json:"already"`
	PauseMs             int64  `json:"pause_ms"`
	SnapshotMs          int64  `json:"snapshot_ms"`
	TotalMs             int64  `json:"total_ms"`
	SnapshotBytes       int64  `json:"snapshot_bytes"`
	SnapshotBytesOnDisk int64  `json:"snapshot_bytes_on_disk"`
	SnapshotDir         string `json:"snapshot_dir"`
}

type hbRes struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	Already        bool   `json:"already"`
	ResumedFrom    string `json:"resumed_from"`
	RestoreMode    string `json:"restore_mode"`
	RestoreMs      int64  `json:"restore_ms"`
	AgentReadyMs   int64  `json:"agent_ready_ms"`
	TotalMs        int64  `json:"total_ms"`
	FallbackReason string `json:"fallback_reason"`
	ClockSkewMs    *int64 `json:"clock_skew_ms"`
}

func TestHibernateResumeLive(t *testing.T) {
	c := newHBCLI(t)
	const ref = "hb15/live"
	step := func(name string, f func(t *testing.T)) {
		t.Helper()
		if !t.Run(name, f) {
			t.Fatalf("step %q failed; aborting", name)
		}
	}

	var id string
	t.Cleanup(func() {
		if !t.Failed() {
			defer os.RemoveAll(filepath.Dir(c.root)) // our own MkdirTemp dir (hb15-live-*)
		}
		if id == "" {
			return
		}
		c.run(2*time.Minute, "stop", ref)
		c.run(2*time.Minute, "rm", ref)
		killIDProcs(t, id)
		if left := procsWithID(id); len(left) != 0 {
			t.Errorf("processes left behind for %s: %v", id, left)
		}
	})

	var sleepPID, sleepStart, uptimeBefore string
	var hostPort uint16
	type nums struct{ pause, snap, total, bytes, disk, restore, agent, http, rss, skew []int64 }
	var n nums

	step("1_create_start_with_state", func(t *testing.T) {
		start := time.Now()
		r := c.run(20*time.Minute, "create", ref, "--file", c.ws, "--memory", "512", "--json")
		if r.code != 0 || r.env.Kind != "sandbox.created" {
			t.Fatalf("create: rc=%d stderr=%s", r.code, hbTail(r.stderr))
		}
		var d struct {
			ID    string `json:"id"`
			State string `json:"state"`
		}
		r.data(&d)
		id = d.ID
		t.Logf("created %s state=%s in %s", id, d.State, time.Since(start))
		c.guest(ref, "echo marker-hb15 > /root/marker; echo shm-hb15 > /dev/shm/hb15; "+
			"setsid sleep 100000 >/dev/null 2>&1 </dev/null & echo $! > /root/sleep.pid; "+
			"cd /www && setsid python3 -m http.server 8080 >/tmp/http.log 2>&1 </dev/null &")
		time.Sleep(time.Second)
		sleepPID = c.guest(ref, "cat /root/sleep.pid")
		sleepStart = c.guest(ref, "awk '{print $22}' /proc/"+sleepPID+"/stat")
		uptimeBefore = c.guest(ref, "cut -d' ' -f1 /proc/uptime")
		hostPort = c.hostPortFor(id, 8080, 90*time.Second)
		body, _, err := httpGet(hostPort, 30*time.Second)
		if err != nil || body != "hello-hb15" {
			t.Fatalf("pre-hibernate http: body=%q err=%v", body, err)
		}
		t.Logf("forward guest 8080 -> host %d; sleep pid=%s starttime=%s uptime=%s", hostPort, sleepPID, sleepStart, uptimeBefore)
		if _, ok := findProc(id, "cloud-hypervisor"); !ok {
			t.Fatalf("no cloud-hypervisor for %s while running; procs=%v", id, procsWithID(id))
		}
	})

	// hibernateCycle hibernates and checks the on-host invariants.
	hibernate := func(t *testing.T) hbHib {
		r := c.run(5*time.Minute, "hibernate", ref, "--json")
		if r.code != 0 {
			t.Fatalf("hibernate rc=%d err=%s %s", r.code, r.env.Error.Message, hbTail(r.stderr))
		}
		var h hbHib
		r.data(&h)
		if h.State != "hibernated" || h.ID != id || h.Already {
			t.Fatalf("hibernate result %+v", h)
		}
		if h.SnapshotBytes <= 0 || h.TotalMs <= 0 || h.SnapshotMs <= 0 {
			t.Fatalf("missing timings/bytes %+v", h)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if len(procsWithID(id)) == 0 {
				return h
			}
			time.Sleep(100 * time.Millisecond)
		}
		for _, p := range procsWithID(id) {
			t.Errorf("process still alive after hibernate: pid %d RSS %d KiB: %.200s", p.pid, vmRSSKiB(p.pid), p.cmd)
		}
		t.FailNow()
		return h
	}
	resume := func(t *testing.T) (hbRes, time.Duration) {
		start := time.Now()
		r := c.run(5*time.Minute, "resume", ref, "--json")
		if r.code != 0 {
			t.Fatalf("resume rc=%d err=%s %s", r.code, r.env.Error.Message, hbTail(r.stderr))
		}
		var s hbRes
		r.data(&s)
		_, _, err := httpGet(hostPort, 60*time.Second)
		if err != nil {
			t.Fatalf("http via forwarded host port %d after resume: %v", hostPort, err)
		}
		return s, time.Since(start)
	}

	step("2_hibernate", func(t *testing.T) {
		h := hibernate(t)
		t.Logf("hibernate: %+v", h)
		n.pause = append(n.pause, h.PauseMs)
		n.snap = append(n.snap, h.SnapshotMs)
		n.total = append(n.total, h.TotalMs)
		n.bytes = append(n.bytes, h.SnapshotBytes)
		n.disk = append(n.disk, h.SnapshotBytesOnDisk)

		r := c.run(time.Minute, "ls", "--json")
		var l struct {
			Sandboxes []struct {
				ID            string `json:"id"`
				State         string `json:"state"`
				SnapshotBytes int64  `json:"snapshot_bytes"`
			} `json:"sandboxes"`
		}
		r.data(&l)
		found := false
		for _, s := range l.Sandboxes {
			if s.ID == id {
				found = true
				if s.State != "hibernated" || s.SnapshotBytes <= 0 {
					t.Fatalf("ls row %+v", s)
				}
			}
		}
		if !found {
			t.Fatalf("sandbox not in ls: %s", r.stdout)
		}
		if _, err := os.Stat(filepath.Join(c.root, "nexus", "supervisors", id, "portfwd-map.json")); err != nil {
			t.Fatalf("portfwd-map.json not persisted in supervisor dir: %v", err)
		}
		r2 := c.run(time.Minute, "hibernate", ref, "--json")
		var h2 hbHib
		r2.data(&h2)
		if r2.code != 0 || !h2.Already {
			t.Fatalf("second hibernate: rc=%d %+v", r2.code, h2)
		}
	})

	step("3_resume_in_place", func(t *testing.T) {
		hostBefore := time.Now()
		s, httpTotal := resume(t)
		t.Logf("resume: %+v http-total(resume start->200)=%s", s, httpTotal)
		if s.ID != id || s.State != "running" || s.Already {
			t.Fatalf("resume result %+v", s)
		}
		if s.ResumedFrom != "snapshot" {
			t.Fatalf("resumed_from=%q fallback=%q (want snapshot)", s.ResumedFrom, s.FallbackReason)
		}
		if s.RestoreMs <= 0 || s.ClockSkewMs == nil {
			t.Fatalf("missing restore_ms/clock_skew_ms: %+v", s)
		}
		n.restore = append(n.restore, s.RestoreMs)
		n.agent = append(n.agent, s.AgentReadyMs)
		n.http = append(n.http, httpTotal.Milliseconds())
		n.skew = append(n.skew, *s.ClockSkewMs)
		_ = hostBefore

		if got := c.guest(ref, "cat /root/marker"); got != "marker-hb15" {
			t.Fatalf("marker = %q", got)
		}
		if got := c.guest(ref, "cat /dev/shm/hb15"); got != "shm-hb15" {
			t.Fatalf("/dev/shm = %q (memory not restored)", got)
		}
		if got := c.guest(ref, "cat /root/sleep.pid"); got != sleepPID {
			t.Fatalf("sleep pid file %q != %q", got, sleepPID)
		}
		if got := c.guest(ref, "awk '{print $22}' /proc/"+sleepPID+"/stat"); got != sleepStart {
			t.Fatalf("sleep starttime %q != %q: process not preserved", got, sleepStart)
		}
		up := c.guest(ref, "cut -d' ' -f1 /proc/uptime")
		ub, _ := strconv.ParseFloat(uptimeBefore, 64)
		ua, _ := strconv.ParseFloat(up, 64)
		if ua <= ub {
			t.Fatalf("guest uptime %s <= %s before hibernate: cold boot?", up, uptimeBefore)
		}
		g0 := time.Now()
		gs := c.guest(ref, "date +%s.%N")
		g1 := time.Now()
		gv, _ := strconv.ParseFloat(gs, 64)
		mid := float64(g0.UnixNano()+g1.UnixNano()) / 2e9
		if d := gv - mid; d > 1 || d < -1 {
			t.Fatalf("guest clock off host by %.3fs", d)
		}
		t.Logf("guest clock delta vs host: %.3fs", gv-mid)
		if p, ok := findProc(id, "cloud-hypervisor"); ok {
			rss := vmRSSKiB(p.pid)
			n.rss = append(n.rss, rss)
			t.Logf("VMM pid %d RSS after resume+checks: %d KiB", p.pid, rss)
		} else {
			t.Fatal("no VMM after resume")
		}
		// Same forwarded host port, persisted map.
		if got := c.hostPortFor(id, 8080, 60*time.Second); got != hostPort {
			t.Fatalf("host port changed %d -> %d", hostPort, got)
		}
		r := c.run(time.Minute, "resume", ref, "--json")
		var s2 hbRes
		r.data(&s2)
		if r.code != 0 || !s2.Already {
			t.Fatalf("second resume: rc=%d %+v", r.code, s2)
		}
	})

	step("4_manifest_ch_version", func(t *testing.T) {
		// Cycle again and read the manifest the driver wrote.
		h := hibernate(t)
		b, err := os.ReadFile(filepath.Join(h.SnapshotDir, "snap", "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			CHVersion string `json:"ch_version"`
		}
		_ = json.Unmarshal(b, &m)
		t.Logf("manifest ch_version=%q", m.CHVersion)
		if m.CHVersion == "" {
			t.Fatal("manifest has no ch_version")
		}
		s, _ := resume(t)
		if s.ResumedFrom != "snapshot" {
			t.Fatalf("restore after version-checked cycle fell back: %+v", s)
		}
		n.pause = append(n.pause, h.PauseMs)
		n.snap = append(n.snap, h.SnapshotMs)
		n.total = append(n.total, h.TotalMs)
		n.bytes = append(n.bytes, h.SnapshotBytes)
		n.disk = append(n.disk, h.SnapshotBytesOnDisk)
		n.restore = append(n.restore, s.RestoreMs)
		n.agent = append(n.agent, s.AgentReadyMs)
	})

	step("4b_third_cycle_numbers", func(t *testing.T) {
		h := hibernate(t)
		s, httpTotal := resume(t)
		if s.ResumedFrom != "snapshot" {
			t.Fatalf("%+v", s)
		}
		n.pause = append(n.pause, h.PauseMs)
		n.snap = append(n.snap, h.SnapshotMs)
		n.total = append(n.total, h.TotalMs)
		n.bytes = append(n.bytes, h.SnapshotBytes)
		n.disk = append(n.disk, h.SnapshotBytesOnDisk)
		n.restore = append(n.restore, s.RestoreMs)
		n.agent = append(n.agent, s.AgentReadyMs)
		n.http = append(n.http, httpTotal.Milliseconds())
		if got := c.guest(ref, "cat /root/marker"); got != "marker-hb15" {
			t.Fatalf("marker = %q", got)
		}
	})

	step("5a_ondemand_unavailable", func(t *testing.T) {
		hibernate(t)
		r := c.run(5*time.Minute, "resume", ref, "--restore-mode", "ondemand", "--no-cold-fallback", "--json")
		t.Logf("strict ondemand: rc=%d code=%s msg=%s", r.code, r.env.Error.Code, r.env.Error.Message)
		if r.code != 1 || r.env.Error.Code != "snapshot_failed" ||
			!strings.Contains(strings.ToLower(r.env.Error.Message), "userfaultfd") &&
				!strings.Contains(strings.ToLower(r.env.Error.Message), "ondemand") {
			t.Fatalf("want exit 1 snapshot_failed mentioning ondemand/userfaultfd")
		}
		// Record stays hibernated, snapshot still usable (pre-attempt refusal).
		r = c.run(5*time.Minute, "resume", ref, "--restore-mode", "ondemand", "--json")
		var s hbRes
		r.data(&s)
		t.Logf("lenient ondemand: rc=%d %+v", r.code, s)
		if r.code != 0 || s.State != "running" {
			t.Fatalf("lenient ondemand resume did not end Running: %s", r.stdout)
		}
		if s.ResumedFrom == "cold" && s.FallbackReason == "" {
			t.Fatal("cold without fallback_reason")
		}
		t.Logf("ondemand lenient: resumed_from=%s reason=%q", s.ResumedFrom, s.FallbackReason)
	})

	step("5b_corrupt_snapshot", func(t *testing.T) {
		// Ensure we are running with state, hibernate, then corrupt.
		cur := c.run(time.Minute, "resume", ref, "--json") // no-op if running
		_ = cur
		h := hibernate(t)
		committed := filepath.Join(h.SnapshotDir, "snap", "COMMITTED")
		if err := os.Remove(committed); err != nil {
			t.Fatal(err)
		}
		r := c.run(5*time.Minute, "resume", ref, "--no-cold-fallback", "--json")
		if r.code != 1 || r.env.Error.Code != "snapshot_failed" {
			t.Fatalf("--no-cold-fallback on invalid snapshot: rc=%d code=%s", r.code, r.env.Error.Code)
		}
		start := time.Now()
		r = c.run(10*time.Minute, "resume", ref, "--json")
		cold := time.Since(start)
		var s hbRes
		r.data(&s)
		if r.code != 0 || s.ResumedFrom != "cold" || s.FallbackReason == "" || s.State != "running" {
			t.Fatalf("cold fallback: rc=%d %+v", r.code, s)
		}
		t.Logf("COLD fallback wall=%s total_ms=%d reason=%q", cold, s.TotalMs, s.FallbackReason)
		if s.TotalMs <= 0 {
			t.Fatalf("cold fallback total_ms not reported: %+v", s)
		}
		if got := c.guest(ref, "cat /root/marker"); got != "marker-hb15" {
			t.Fatalf("disk lost across cold fallback: marker=%q", got)
		}
		if got := c.guest(ref, "cat /dev/shm/hb15 2>/dev/null || echo gone"); got != "gone" {
			t.Fatalf("/dev/shm survived cold start?? %q", got)
		}
	})

	step("6_kill_mid_hibernate", func(t *testing.T) {
		vmm, ok := findProc(id, "cloud-hypervisor")
		sup, ok2 := findProc(id, "__supervisor")
		if !ok || !ok2 {
			t.Fatalf("need VMM+supervisor: %v", procsWithID(id))
		}
		// Freeze the VMM so the supervisor blocks inside its hibernate
		// pause/snapshot calls, then SIGKILL VMM and supervisor mid-flight.
		if err := syscall.Kill(vmm.pid, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		done := make(chan *hbResult, 1)
		go func() { done <- c.run(2*time.Minute, "hibernate", ref, "--json") }()
		time.Sleep(3 * time.Second)
		for _, p := range []hbProc{vmm, sup} {
			raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.pid))
			if !strings.Contains(string(raw), id) {
				t.Fatalf("pid %d no longer ours", p.pid)
			}
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
		killIDProcs(t, id) // remaining helpers (virtiofsd, netns child) of OUR sandbox
		select {
		case r := <-done:
			t.Logf("interrupted hibernate: rc=%d err=%s", r.code, r.env.Error.Message)
		case <-time.After(2 * time.Minute):
			t.Fatal("hibernate client hung after kill")
		}
		r := c.run(time.Minute, "ls", "--json")
		t.Logf("ls after crash: %s", strings.TrimSpace(r.stdout))
		start := time.Now()
		r = c.run(10*time.Minute, "resume", ref, "--json")
		var s hbRes
		r.data(&s)
		t.Logf("resume after crash: rc=%d wall=%s %+v", r.code, time.Since(start), s)
		if r.code != 0 {
			t.Fatalf("resume after mid-hibernate crash failed: %s", r.stdout)
		}
		if s.State != "running" {
			t.Fatalf("state=%s", s.State)
		}
		if s.Already {
			// A dead Running record must not be reported as already running.
			t.Fatalf("resume said already:true although VM was killed")
		}
		if s.ResumedFrom != "cold" || s.FallbackReason == "" {
			t.Logf("note: resumed_from=%q reason=%q", s.ResumedFrom, s.FallbackReason)
		}
		if got := c.guest(ref, "cat /root/marker"); got != "marker-hb15" {
			t.Fatalf("disk lost after crash: marker=%q", got)
		}
		if _, ok := findProc(id, "cloud-hypervisor"); !ok {
			t.Fatal("no VMM after post-crash resume")
		}
		if ents, err := os.ReadDir(filepath.Join(c.root, "nexus", "supervisors", id, "hibernate")); err == nil {
			for _, e := range ents {
				t.Logf("post-crash hibernate dir entry: %s", e.Name())
			}
		}
	})

	step("7_stop_removes_snapshot_rm", func(t *testing.T) {
		h := hibernate(t)
		if _, err := os.Stat(h.SnapshotDir); err != nil {
			t.Fatalf("snapshot dir missing: %v", err)
		}
		r := c.run(2*time.Minute, "stop", ref)
		if r.code != 0 {
			t.Fatalf("stop on hibernated: rc=%d %s", r.code, r.stderr)
		}
		if _, err := os.Stat(h.SnapshotDir); !os.IsNotExist(err) {
			t.Fatalf("snapshot dir still present after stop (err=%v)", err)
		}
		r = c.run(2*time.Minute, "rm", ref)
		if r.code != 0 {
			t.Fatalf("rm rc=%d %s", r.code, r.stderr)
		}
		killIDProcs(t, id)
		if left := procsWithID(id); len(left) != 0 {
			t.Fatalf("leftover procs %v", left)
		}
		id = "" // cleanup has nothing to do
	})

	t.Logf("NUMBERS (n=%d cycles): pause_ms=%v snapshot_ms=%v total_ms=%v snapshot_bytes=%v on_disk=%v restore_ms=%v agent_ready_ms=%v resume_to_http200_ms=%v vmm_rss_kib=%v clock_skew_ms=%v",
		len(n.total), n.pause, n.snap, n.total, n.bytes, n.disk, n.restore, n.agent, n.http, n.rss, n.skew)
	if len(n.total) > 0 && len(n.restore) > 0 {
		t.Logf("MEDIAN: pause=%d snapshot=%d total=%d bytes=%d on_disk=%d restore=%d agent=%d http=%d",
			median(n.pause), median(n.snap), median(n.total), median(n.bytes), median(n.disk), median(n.restore), median(n.agent), median(n.http))
	}
}

func hbTail(s string) string {
	if len(s) > 1500 {
		return s[len(s)-1500:]
	}
	return s
}
