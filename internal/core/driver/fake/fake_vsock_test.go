package fake_test

import (
	"errors"
	"io"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

// TestFakeDialGuest_RoundTrip verifies that DialGuest returns a working
// bidirectional net.Conn and that Capabilities reports GuestDialer.
func TestFakeDialGuest_RoundTrip(t *testing.T) {
	f := fake.New()
	id := newID()

	// Capabilities must report GuestDialer.
	caps := driver.Capabilities(f)
	hasGD := false
	for _, c := range caps {
		if c == "GuestDialer" {
			hasGD = true
			break
		}
	}
	if !hasGD {
		t.Fatalf("Capabilities() = %v, want to include GuestDialer", caps)
	}

	// DialGuest must return a usable conn.
	hostConn, err := f.DialGuest(ctx, id, driver.AgentControlPort)
	if err != nil {
		t.Fatalf("DialGuest: %v", err)
	}
	t.Cleanup(func() { hostConn.Close() })

	guestConn := f.GuestConn(id)
	if guestConn == nil {
		t.Fatal("GuestConn returned nil after DialGuest")
	}
	t.Cleanup(func() { guestConn.Close() })

	// --- host → guest ---
	want := "ping"
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, len(want))
		_, err := io.ReadFull(guestConn, buf)
		if err != nil {
			done <- err
			return
		}
		if string(buf) != want {
			done <- errors.New("guest read: got " + string(buf) + ", want " + want)
			return
		}
		done <- nil
	}()

	if _, err := io.WriteString(hostConn, want); err != nil {
		t.Fatalf("host write: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("host→guest: %v", err)
	}

	// --- guest → host ---
	reply := "pong"
	go func() {
		_, err := io.WriteString(guestConn, reply)
		done <- err
	}()

	buf := make([]byte, len(reply))
	if _, err := io.ReadFull(hostConn, buf); err != nil {
		t.Fatalf("host read: %v", err)
	}
	if string(buf) != reply {
		t.Fatalf("guest→host: got %q, want %q", string(buf), reply)
	}
	if err := <-done; err != nil {
		t.Fatalf("guest write: %v", err)
	}
}

// TestFakeDialGuest_InjectedError verifies that SetDialGuestError causes
// DialGuest to fail, and that Capabilities still reports GuestDialer.
func TestFakeDialGuest_InjectedError(t *testing.T) {
	f := fake.New()
	id := newID()
	want := errors.New("injected dial error")
	f.SetDialGuestError(want)

	conn, err := f.DialGuest(ctx, id, driver.AgentControlPort)
	if conn != nil {
		conn.Close()
		t.Fatal("expected nil conn on injected error")
	}
	if !errors.Is(err, want) {
		t.Fatalf("got error %v, want %v", err, want)
	}
}

// TestFakeDialGuest_CallLogged verifies that DialGuest is recorded in the
// call log.
func TestFakeDialGuest_CallLogged(t *testing.T) {
	f := fake.New()
	id := newID()

	hostConn, err := f.DialGuest(ctx, id, driver.AgentControlPort)
	if err != nil {
		t.Fatalf("DialGuest: %v", err)
	}
	hostConn.Close()
	if gc := f.GuestConn(id); gc != nil {
		gc.Close()
	}

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Kind != fake.CallDialGuest {
		t.Fatalf("expected CallDialGuest, got %v", calls[0].Kind)
	}
	if calls[0].ID != id {
		t.Fatalf("call ID mismatch: got %v, want %v", calls[0].ID, id)
	}
}
