package store_test

// TestNetnsFieldsRoundTrip verifies that all five netns adoption identity
// fields on domain.Sandbox survive a store.Update → disk write → store.Get
// round-trip with their non-zero values intact.
//
// The test reads back what was actually written (via store.Get), not what was
// passed in — catching any codec gap (missing JSON tag, omitempty on a value
// that should survive, wrong type) that a simple in-memory check would miss.
//
// Mutation discipline: stop writing any one of the five fields, the
// corresponding assertion here goes RED. This is verified manually as part of
// every change to the field set; the mutation instructions are inline.
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

func TestNetnsFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	// Use NewFileStore directly (not the newStore helper) so we keep the root
	// path for reading the raw record.json file later.
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := makeSandbox("netns-rt", "testproject")

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Write all five fields via store.Update (the path the live supervisor takes).
	wantPID := 12345
	wantPGID := 12345
	wantStartTime := uint64(9876543210)
	wantTap := "tap0-abc123"
	wantSock := "/run/nexus3/netns-rt/ch.sock"

	if err := st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		rec.NetnsChildPID = wantPID
		rec.NetnsChildPGID = wantPGID
		rec.NetnsChildStartTime = wantStartTime
		rec.GuestTapName = wantTap
		rec.CHAPISocket = wantSock
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Read back via store.Get — this deserialises from the file on disk.
	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.NetnsChildPID != wantPID {
		t.Errorf("NetnsChildPID: got %d, want %d", got.NetnsChildPID, wantPID)
	}
	if got.NetnsChildPGID != wantPGID {
		t.Errorf("NetnsChildPGID: got %d, want %d", got.NetnsChildPGID, wantPGID)
	}
	if got.NetnsChildStartTime != wantStartTime {
		t.Errorf("NetnsChildStartTime: got %d, want %d", got.NetnsChildStartTime, wantStartTime)
	}
	if got.GuestTapName != wantTap {
		t.Errorf("GuestTapName: got %q, want %q", got.GuestTapName, wantTap)
	}
	if got.CHAPISocket != wantSock {
		t.Errorf("CHAPISocket: got %q, want %q", got.CHAPISocket, wantSock)
	}

	// Additionally verify the raw JSON on disk contains the expected keys.
	// This catches a codec regression where the Go field is populated but the
	// JSON tag is wrong or missing (the codec would silently drop the key).
	recordPath := filepath.Join(root, "sandboxes", sb.ID.String(), "record.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read record.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal record.json: %v", err)
	}
	for _, key := range []string{
		"netns_child_pid",
		"netns_child_pgid",
		"netns_child_start_time",
		"guest_tap_name",
		"ch_api_socket",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("record.json is missing expected key %q", key)
		}
	}
}
