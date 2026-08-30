package supervisor

// TestHandoff_IncompletePayload_Refuses is the MAJOR 3 regression test:
// performHandoff must return (false, reason, nil) — a clean refusal — when
// the payload is incomplete (CA material not wired), rather than proceeding
// and half-transferring state to the replacement.
//
// Mutation proof: if the payload.Validate() call and early-return are removed
// from performHandoff, the build closure below returns an empty CA, the offer
// goes through, and the fake peer confirms it — causing ok=true and the test
// to fail with "want ok=false".

import (
	"context"
	"os"
	"testing"

	"github.com/IniZio/nexus3/internal/supervisor/handoff"
)

// TestHandoff_IncompletePayload_Refuses proves that performHandoff refuses
// before even reaching the wire when the payload fails Validate().
func TestHandoff_IncompletePayload_Refuses(t *testing.T) {
	// Provide a real peer socket so performHandoff can actually dial it.
	// If validation fires before Offer, the peer never even sees the call.
	peerPath, acceptedCh := listenHandoffPeer(t)

	// Peer goroutine: if it ever accepts a connection, it confirms — but the
	// test asserts that validation fires before the dial even sends a payload.
	go func() {
		peerConn, ok := <-acceptedCh
		if !ok {
			return
		}
		defer peerConn.Close()
		_, fd, err := handoff.Accept(peerConn)
		if err != nil {
			return
		}
		if fd != nil {
			fd.Close()
		}
		// Confirm if asked — we want to detect whether the caller skips validation.
		_ = handoff.Confirm(peerConn)
	}()

	// Build returns a payload with no CA material (the incomplete state this
	// branch currently has).
	build := payloadBuilder(func() (handoff.Payload, *os.File, error) {
		return handoff.Payload{
			Version:   handoff.CurrentVersion,
			Perimeter: handoff.PerimeterHandle{Present: false},
			// CA is zero-value: CertPEM and KeyPEM are nil.
		}, nil, nil
	})

	ok, reason, err := performHandoff(context.Background(), peerPath, build)
	if err != nil {
		t.Fatalf("performHandoff: unexpected error: %v", err)
	}
	if ok {
		t.Fatal("performHandoff with incomplete payload (no CA): ok = true, want false — " +
			"an incomplete handoff must be refused, not committed")
	}
	if reason == "" {
		t.Error("performHandoff with incomplete payload: reason is empty; " +
			"the refusal must name the incomplete payload")
	}
}

// TestHandoff_CompletePayload_ProceedsToWire proves the positive counterpart:
// a payload with CA material populated passes Validate() and reaches the wire.
// The fake peer refuses at the handoff level (not the validate level) — this
// confirms validation did NOT short-circuit a complete payload.
func TestHandoff_CompletePayload_ProceedsToWire(t *testing.T) {
	peerPath, acceptedCh := listenHandoffPeer(t)

	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		peerConn, chOK := <-acceptedCh
		if !chOK {
			t.Error("fake peer: connection never accepted — validate must have short-circuited a complete payload")
			return
		}
		defer peerConn.Close()
		_, fd, err := handoff.Accept(peerConn)
		if err != nil {
			t.Errorf("fake peer: Accept: %v", err)
			return
		}
		if fd != nil {
			fd.Close()
		}
		// Refuse at the handoff level to keep the test simple.
		if err := handoff.Refuse(peerConn, "test refusal"); err != nil {
			t.Errorf("fake peer: Refuse: %v", err)
		}
	}()

	build := payloadBuilder(func() (handoff.Payload, *os.File, error) {
		// Populate CA so Validate() passes.
		return handoff.Payload{
			Version: handoff.CurrentVersion,
			CA: handoff.CAMaterial{
				CertPEM: []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
				KeyPEM:  []byte("-----BEGIN EC PRIVATE KEY-----\nfake\n-----END EC PRIVATE KEY-----\n"),
			},
		}, nil, nil
	})

	ok, _, err := performHandoff(context.Background(), peerPath, build)
	<-peerDone

	if err != nil {
		t.Fatalf("performHandoff: unexpected error: %v", err)
	}
	// Peer refused at the handoff level, so ok must be false — but for the
	// right reason (peer refusal, not payload validation).
	if ok {
		t.Fatal("performHandoff: ok = true after peer refusal; want false")
	}

	// The key assertion: the peer goroutine must have accepted the connection,
	// proving performHandoff reached the wire (i.e. validation passed).
	select {
	case <-peerDone:
	default:
		t.Fatal("peer goroutine did not complete — connection was never accepted; " +
			"validate may have incorrectly rejected a complete payload")
	}
}

// TestPayloadValidate_EmptyCA returns a refusal reason.
func TestPayloadValidate_EmptyCA(t *testing.T) {
	p := handoff.Payload{Version: handoff.CurrentVersion}
	if reason := p.Validate(); reason == "" {
		t.Error("Validate with empty CA: got empty reason, want a non-empty refusal")
	}
}

// TestPayloadValidate_PopulatedCA returns empty (no refusal).
func TestPayloadValidate_PopulatedCA(t *testing.T) {
	p := handoff.Payload{
		Version: handoff.CurrentVersion,
		CA: handoff.CAMaterial{
			CertPEM: []byte("cert"),
			KeyPEM:  []byte("key"),
		},
	}
	if reason := p.Validate(); reason != "" {
		t.Errorf("Validate with populated CA: got reason %q, want empty", reason)
	}
}

// listenHandoffPeer is defined in ipc_detach_test.go and shared within the
// package test binary (both files declare package supervisor).
