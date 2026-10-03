package hubstate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const childEnv = "HUBSTATE_TEST_CHILD_ROOT"

// TestMain: child mode only runs the helper then exits; it never calls m.Run,
// so a re-exec cannot recurse into the suite.
func TestMain(m *testing.M) {
	if root := os.Getenv(childEnv); root != "" {
		os.Exit(childMain(root))
	}
	os.Exit(m.Run())
}

// childMain waits for "go" on stdin, claims, prints the result as JSON, then
// stays alive until stdin closes so the occupant remains live.
func childMain(root string) int {
	st, err := Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p, err := ProcOf(os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	in := bufio.NewReader(os.Stdin)
	_, _ = in.ReadString('\n')
	res, err := st.Claim(ClaimReq{
		Seat:      os.Getenv("HUBSTATE_TEST_SEAT"),
		Explicit:  os.Getenv("HUBSTATE_TEST_EXPLICIT") == "1",
		SessionID: fmt.Sprintf("s%d", os.Getpid()),
		Proc:      p,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = json.NewEncoder(os.Stdout).Encode(res)
	_, _ = in.ReadString('\n')
	return 0
}

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func concurrentClaims(t *testing.T, explicit bool) {
	root := t.TempDir()
	const n = 20
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type child struct {
		cmd *exec.Cmd
		in  interface{ Write([]byte) (int, error) }
		cl  func() error
		out *bufio.Reader
	}
	var kids []child
	for i := 0; i < n; i++ {
		cmd := exec.Command(exe, "-test.run=^$")
		cmd.Env = append(os.Environ(), childEnv+"="+root, "HUBSTATE_TEST_SEAT=repo", fmt.Sprintf("HUBSTATE_TEST_EXPLICIT=%d", b2i(explicit)))
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		kids = append(kids, child{cmd, in, in.Close, bufio.NewReader(out)})
	}
	for _, k := range kids {
		_, _ = k.in.Write([]byte("go\n"))
	}
	var seats []string
	for _, k := range kids {
		line, err := k.out.ReadString('\n')
		if err != nil {
			t.Fatalf("child read: %v", err)
		}
		var r ClaimResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		seats = append(seats, r.Seat)
	}
	for _, k := range kids {
		_ = k.cl()
		_ = k.cmd.Wait()
	}
	sort.Strings(seats)
	primary := 0
	seen := map[string]bool{}
	for _, s := range seats {
		if seen[s] {
			t.Fatalf("seat %q occupied twice: %v", s, seats)
		}
		seen[s] = true
		if s == "repo" {
			primary++
		}
	}
	if primary != 1 || len(seats) != n {
		t.Fatalf("want exactly one occupant of repo, got %d in %v", primary, seats)
	}
	for i := 1; i < n; i++ {
		if !seen[fmt.Sprintf("repo#%d", i)] {
			t.Fatalf("sub-seats not dense 1..%d: %v", n-1, seats)
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestConcurrentClaimsDefaultSeat(t *testing.T)  { concurrentClaims(t, false) }
func TestConcurrentClaimsExplicitSeat(t *testing.T) { concurrentClaims(t, true) }

func selfProc(t *testing.T) Proc {
	t.Helper()
	p, err := ProcOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPidReuseDifferentStarttimeIsDead(t *testing.T) {
	p := selfProc(t)
	if !Alive(p) {
		t.Fatal("self should be alive")
	}
	r := p
	r.Starttime++
	if Alive(r) {
		t.Fatal("reused pid with new starttime must be dead")
	}
	r = p
	r.BootID = "other-boot"
	if Alive(r) {
		t.Fatal("different boot_id must be dead")
	}
}

func TestDeadOccupantRules(t *testing.T) {
	st := newStore(t)
	me := selfProc(t)
	dead := me
	dead.Starttime++

	// default seat, dead occupant: sub-seat, no inheritance
	if _, err := st.Claim(ClaimReq{Seat: "d", SessionID: "old", Proc: dead}); err != nil {
		t.Fatal(err)
	}
	r, err := st.Claim(ClaimReq{Seat: "d", SessionID: "new", Proc: me})
	if err != nil || !r.SubSeat || r.Seat != "d#1" || r.Inherited || r.PredecessorLive {
		t.Fatalf("default: %+v %v", r, err)
	}

	// explicit seat, dead occupant: takeover with inheritance
	if _, err := st.Claim(ClaimReq{Seat: "e#x", Explicit: true, SessionID: "old", Proc: dead}); err != nil {
		t.Fatal(err)
	}
	r, err = st.Claim(ClaimReq{Seat: "e#x", Explicit: true, SessionID: "new", Proc: me})
	if err != nil || r.SubSeat || !r.Inherited || r.Seat != "e#x" {
		t.Fatalf("explicit: %+v %v", r, err)
	}

	// explicit request against a live occupant: sub-seat
	r, err = st.Claim(ClaimReq{Seat: "e#x", Explicit: true, SessionID: "third", Proc: Proc{PID: 1, Starttime: 1, BootID: me.BootID}})
	if err != nil || !r.SubSeat || !r.PredecessorLive {
		t.Fatalf("live: %+v %v", r, err)
	}

	// seat take: dead default occupant can be taken and becomes explicit
	if _, err := st.Take("d", "new", me); err != nil {
		t.Fatal(err)
	}
	s, _ := st.GetSeat("d")
	if !s.Explicit || s.Occupant.SessionID != "new" {
		t.Fatalf("take: %+v", s)
	}
	live := Proc{PID: 1, Starttime: 1, BootID: me.BootID}
	if _, err := st.Take("e#x", "z", live); err != nil {
		t.Fatalf("take live: %v", err)
	}
	prev, err := st.Take("e#x", "y", me)
	if err != nil || prev.SessionID != "z" {
		t.Fatalf("displace live: prev=%+v err=%v", prev, err)
	}
	if s, _ := st.GetSeat("e#x"); s.Occupant.SessionID != "y" || !s.Explicit {
		t.Fatalf("after displace: %+v", s)
	}
}

func TestSubSeatsMonotonicNeverReused(t *testing.T) {
	st := newStore(t)
	me := selfProc(t)
	dead := me
	dead.Starttime++
	if _, err := st.Claim(ClaimReq{Seat: "r", SessionID: "a", Proc: me}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 4; i++ {
		// each sub-seat holder is dead, yet the number must advance
		r, err := st.Claim(ClaimReq{Seat: "r", SessionID: fmt.Sprintf("s%d", i), Proc: Proc{PID: 1, Starttime: uint64(i + 1), BootID: dead.BootID}})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, r.Seat)
	}
	if strings.Join(got, ",") != "r#1,r#2,r#3,r#4" {
		t.Fatalf("got %v", got)
	}
}

func TestClaimIdempotentForSameSession(t *testing.T) {
	st := newStore(t)
	me := selfProc(t)
	for i := 0; i < 3; i++ {
		r, err := st.Claim(ClaimReq{Seat: "r", SessionID: "a", Proc: me})
		if err != nil || r.Seat != "r" || r.SubSeat {
			t.Fatalf("%+v %v", r, err)
		}
	}
}

func TestSessions(t *testing.T) {
	st := newStore(t)
	me := selfProc(t)
	if err := st.Heartbeat("nope"); err != ErrNotFound {
		t.Fatalf("got %v", err)
	}
	if err := st.RegisterSession(Session{ID: "s1", Kind: "claude", Proc: me}); err != nil {
		t.Fatal(err)
	}
	a, _ := st.LookupSession("s1")
	if err := st.Heartbeat("s1"); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSession(Session{ID: "s1", Kind: "claude", Proc: me}); err != nil {
		t.Fatal(err)
	}
	b, _ := st.LookupSession("s1")
	if !b.Started.Equal(a.Started) || b.Heartbeat.Before(a.Heartbeat) {
		t.Fatalf("started/heartbeat: %+v %+v", a, b)
	}
	if live, _ := st.SessionLive("s1"); !live {
		t.Fatal("want live")
	}
	if _, err := st.LookupSession("../x"); err == nil {
		t.Fatal("want invalid name")
	}
}

func TestStatfsRefusal(t *testing.T) {
	for _, typ := range []int64{0x65735546, 0x01021997, 0x6969} {
		_, err := open(t.TempDir(), func(string) (int64, error) { return typ, nil })
		if err == nil || !strings.Contains(err.Error(), "non-local") {
			t.Fatalf("type %#x: %v", typ, err)
		}
	}
	if _, err := open(t.TempDir(), func(string) (int64, error) { return 0xEF53, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestLayoutAndAtomicNoTmpLeft(t *testing.T) {
	st := newStore(t)
	for _, d := range domains {
		if fi, err := os.Stat(filepath.Join(st.Root(), d)); err != nil || !fi.IsDir() {
			t.Fatalf("missing %s", d)
		}
	}
	p := filepath.Join(st.Root(), "seats", "x")
	if err := WriteFileAtomic(p, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("leftover tmp: %v", ents)
	}
}

func TestLockTimeoutWhileHeld(t *testing.T) {
	st := newStore(t)
	old := lockWait
	lockWait = 50 * time.Millisecond
	defer func() { lockWait = old }()
	unlock, err := st.Lock("budget")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Lock("budget"); err != ErrLockTimeout {
		t.Fatalf("got %v", err)
	}
	unlock()
	u, err := st.Lock("budget")
	if err != nil {
		t.Fatal(err)
	}
	u()
}
