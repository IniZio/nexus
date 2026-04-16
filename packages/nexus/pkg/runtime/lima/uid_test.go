package lima

import (
	"os"
	"testing"
)

func TestHostUID(t *testing.T) {
	uid := HostUID()
	if uid <= 0 {
		t.Fatalf("expected positive UID, got %d", uid)
	}
	if uid != os.Getuid() {
		t.Fatalf("HostUID() = %d, want %d", uid, os.Getuid())
	}
}
