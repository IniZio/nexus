package agentpb_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

// TestExecRequestRoundTrip marshals an ExecRequest to bytes and unmarshals it
// back, asserting that the reconstructed message equals the original.
func TestExecRequestRoundTrip(t *testing.T) {
	original := &agentpb.ExecRequest{
		SessionId: "host-minted-session-abc123",
		Argv:      []string{"/bin/bash", "-c", "echo hello"},
		Env:       map[string]string{"FOO": "bar", "HOME": "/root"},
		Cwd:       "/workspace",
		Pty: &agentpb.PtyOptions{
			Term: "xterm-256color",
			InitialSize: &agentpb.WinSize{
				Rows:    24,
				Cols:    80,
				XPixels: 0,
				YPixels: 0,
			},
		},
	}

	b, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	got := &agentpb.ExecRequest{}
	if err := proto.Unmarshal(b, got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if !proto.Equal(original, got) {
		t.Errorf("roundtrip mismatch:\n  original: %v\n  got:      %v", original, got)
	}
}

// TestCopyRequestRoundTrip round-trips a CopyRequest to exercise the Copy
// negotiation message path.
func TestCopyRequestRoundTrip(t *testing.T) {
	original := &agentpb.CopyRequest{
		Direction:   agentpb.CopyDirection_COPY_DIRECTION_PULL,
		GuestPath:   "/workspace/output",
		IsDirectory: true,
	}

	b, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	got := &agentpb.CopyRequest{}
	if err := proto.Unmarshal(b, got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if !proto.Equal(original, got) {
		t.Errorf("roundtrip mismatch:\n  original: %v\n  got:      %v", original, got)
	}
}

// TestSessionInfoRoundTrip round-trips a SessionInfo to exercise the enum
// and exit-code fields.
func TestSessionInfoRoundTrip(t *testing.T) {
	original := &agentpb.SessionInfo{
		SessionId: "sess-xyz",
		State:     agentpb.SessionState_SESSION_STATE_EXITED,
		Pid:       1234,
		ExitCode:  0,
	}

	b, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	got := &agentpb.SessionInfo{}
	if err := proto.Unmarshal(b, got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if !proto.Equal(original, got) {
		t.Errorf("roundtrip mismatch:\n  original: %v\n  got:      %v", original, got)
	}
}
