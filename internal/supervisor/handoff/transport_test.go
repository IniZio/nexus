package handoff

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// unixgramPair creates two connected AF_UNIX SOCK_DGRAM *net.UnixConn via
// socketpair(2), mirroring cloudhypervisor.unixgramPair. No root, no VM: a
// hermetic stand-in for the real supervisor-to-supervisor IPC socket.
func unixgramPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}

	fileA := os.NewFile(uintptr(fds[0]), "handoff-test-a")
	fileB := os.NewFile(uintptr(fds[1]), "handoff-test-b")

	connA, err := net.FileConn(fileA)
	fileA.Close()
	if err != nil {
		fileB.Close()
		t.Fatalf("FileConn[0]: %v", err)
	}
	connB, err := net.FileConn(fileB)
	fileB.Close()
	if err != nil {
		connA.Close()
		t.Fatalf("FileConn[1]: %v", err)
	}

	t.Cleanup(func() {
		connA.Close()
		connB.Close()
	})

	uA, ok := connA.(*net.UnixConn)
	if !ok {
		t.Fatalf("connA is %T, want *net.UnixConn", connA)
	}
	uB, ok := connB.(*net.UnixConn)
	if !ok {
		t.Fatalf("connB is %T, want *net.UnixConn", connB)
	}
	return uA, uB
}

// tempFileWithContent creates a temp file, writes content, and returns it
// seeked back to 0 so the receiving side can read it fresh.
func tempFileWithContent(t *testing.T, content string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "handoff-fd-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	return f
}

// TestRoundTrip_AcceptedHandoff proves an fd sent via SCM_RIGHTS is usable
// on the receiving side, end to end through Offer/Accept/Confirm, alongside
// the non-fd adoption state (virtiofsd handles, CA material, credentials,
// governor config).
func TestRoundTrip_AcceptedHandoff(t *testing.T) {
	const wantContent = "perimeter fd payload contents\n"
	srcFile := tempFileWithContent(t, wantContent)

	outgoing, incoming := unixgramPair(t)

	sent := Payload{
		Version:   CurrentVersion,
		Perimeter: PerimeterHandle{Present: true},
		Virtiofs: []VirtiofsHandle{
			{PID: 4242, SocketPath: "/run/nexus3/sb1/virtiofs-0.sock", SharedDir: "/workspace", ReadOnly: false},
		},
		CA: CAMaterial{
			CertPEM: []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
			KeyPEM:  []byte("-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----\n"),
		},
		Credentials: map[string]string{"ph_abc123": "ghp_realtoken"},
		Governor:    GovernorConfig{VCPUCount: 4, MemoryMB: 8192},
	}

	var (
		wg       sync.WaitGroup
		ack      Ack
		offerErr error
	)
	wg.Go(func() {
		ack, offerErr = Offer(outgoing, sent, int(srcFile.Fd()))
	})

	incoming.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, gotFile, err := Accept(incoming)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if gotFile == nil {
		t.Fatal("Accept: got nil fd, want a usable fd")
	}
	defer gotFile.Close()

	if err := Confirm(incoming); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	wg.Wait()
	if offerErr != nil {
		t.Fatalf("Offer: %v", offerErr)
	}
	if !ack.OK {
		t.Fatalf("Offer returned ack.OK=false, reason=%q", ack.Reason)
	}

	// The payload round-tripped intact.
	if got.Version != sent.Version {
		t.Errorf("Version = %d, want %d", got.Version, sent.Version)
	}
	if len(got.Virtiofs) != 1 || got.Virtiofs[0] != sent.Virtiofs[0] {
		t.Errorf("Virtiofs = %+v, want %+v", got.Virtiofs, sent.Virtiofs)
	}
	if !bytes.Equal(got.CA.CertPEM, sent.CA.CertPEM) || !bytes.Equal(got.CA.KeyPEM, sent.CA.KeyPEM) {
		t.Errorf("CA material did not round-trip")
	}
	if got.Credentials["ph_abc123"] != "ghp_realtoken" {
		t.Errorf("Credentials did not round-trip: %+v", got.Credentials)
	}
	if got.Governor != sent.Governor {
		t.Errorf("Governor = %+v, want %+v", got.Governor, sent.Governor)
	}

	// The fd is a genuinely separate, usable descriptor: read it back and
	// confirm the receiving side sees the exact bytes the sender wrote,
	// independent of the sender's own copy.
	buf := make([]byte, len(wantContent))
	if _, err := gotFile.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt on received fd: %v", err)
	}
	if string(buf) != wantContent {
		t.Errorf("received fd content = %q, want %q", buf, wantContent)
	}

	// Writing through the received fd is visible via the original path too
	// (they share the same underlying file description), proving this is a
	// real SCM_RIGHTS transfer and not, say, a coincidentally-matching copy.
	// WriteAt (not WriteString) makes the write position explicit rather than
	// relying on the shared file offset, which the fd inherits from srcFile.
	if _, err := gotFile.WriteAt([]byte("appended-by-receiver"), int64(len(wantContent))); err != nil {
		t.Fatalf("WriteAt on received fd: %v", err)
	}
	roundTrip := make([]byte, len(wantContent)+len("appended-by-receiver"))
	if _, err := srcFile.ReadAt(roundTrip, 0); err != nil {
		t.Fatalf("ReadAt on sender's original fd: %v", err)
	}
	if string(roundTrip) != wantContent+"appended-by-receiver" {
		t.Errorf("sender's fd did not observe receiver's write: got %q", roundTrip)
	}
}

// TestRoundTrip_VersionRefusalIsResumable proves that when the incoming
// side does not understand the payload's version, it refuses in a way the
// outgoing side can read as a definite, immediate "you still own
// everything" — D-HSH-08's resumable-failure requirement. The outgoing
// side's own fd is untouched by the refusal (SCM_RIGHTS only ever
// duplicates), so resumability here reduces to: the outgoing side must be
// able to observe the refusal and must not have closed anything before
// seeing it.
func TestRoundTrip_VersionRefusalIsResumable(t *testing.T) {
	srcFile := tempFileWithContent(t, "still mine\n")
	outgoing, incoming := unixgramPair(t)

	const futureVersion = CurrentVersion + 999
	sent := Payload{
		Version:   futureVersion,
		Perimeter: PerimeterHandle{Present: true},
	}

	var (
		wg       sync.WaitGroup
		ack      Ack
		offerErr error
	)
	wg.Go(func() {
		ack, offerErr = Offer(outgoing, sent, int(srcFile.Fd()))
	})

	incoming.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, gotFile, err := Accept(incoming)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if gotFile != nil {
		gotFile.Close() // dup'd; harmless to close, sender's fd is unaffected
	}

	if got.Version != futureVersion {
		t.Fatalf("Version = %d, want %d", got.Version, futureVersion)
	}
	// The incoming side does not understand this version: refuse rather
	// than guess at the payload's layout.
	if got.Version != CurrentVersion {
		if err := Refuse(incoming, "unsupported handoff version"); err != nil {
			t.Fatalf("Refuse: %v", err)
		}
	}

	wg.Wait()
	if offerErr != nil {
		t.Fatalf("Offer: %v", offerErr)
	}
	if ack.OK {
		t.Fatal("ack.OK = true, want false for an unsupported version")
	}
	if ack.SupportedVersion != CurrentVersion {
		t.Errorf("ack.SupportedVersion = %d, want %d", ack.SupportedVersion, CurrentVersion)
	}

	// Resumability: the outgoing side's original fd is still fully live and
	// exclusively owned by it — nothing here ever closed or transferred
	// away sole ownership. It can carry on serving as if no handoff was
	// attempted.
	buf := make([]byte, len("still mine\n"))
	if _, err := srcFile.ReadAt(buf, 0); err != nil {
		t.Fatalf("outgoing side's fd is no longer usable after a refusal: %v", err)
	}
	if string(buf) != "still mine\n" {
		t.Errorf("outgoing side's fd content changed after refusal: got %q", buf)
	}
}

// TestAccept_RejectsFDWithoutPresentFlag proves Accept treats a payload
// that mismatches its own Perimeter.Present flag against what actually
// arrived as a protocol error rather than silently trusting one signal over
// the other. It bypasses Offer (which forbids constructing this mismatch)
// to simulate a malformed or hostile peer.
func TestAccept_RejectsFDWithoutPresentFlag(t *testing.T) {
	outgoing, incoming := unixgramPair(t)

	srcFile := tempFileWithContent(t, "x")
	data, err := json.Marshal(Payload{
		Version:   CurrentVersion,
		Perimeter: PerimeterHandle{Present: false}, // claims no fd...
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	oob := syscall.UnixRights(int(srcFile.Fd())) // ...but one is attached anyway.
	if _, _, err := outgoing.WriteMsgUnix(data, oob, nil); err != nil {
		t.Fatalf("WriteMsgUnix: %v", err)
	}

	incoming.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, gotFile, err := Accept(incoming)
	if gotFile != nil {
		gotFile.Close()
	}
	if err == nil {
		t.Fatal("Accept: got nil error, want a protocol-violation error for the Present/fd mismatch")
	}
}
