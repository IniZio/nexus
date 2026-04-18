package update

import (
	"testing"
	"time"
)

func TestShouldSuppressVersion(t *testing.T) {
	state := State{
		AttemptedVersion: "1.2.3",
		LastFailureAt:    time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
		BadVersions:      []string{"1.2.3"},
	}
	if !shouldSuppressVersion(state, "1.2.3", 24*time.Hour) {
		t.Fatalf("expected suppression for cooldown window")
	}
	if shouldSuppressVersion(state, "1.2.3", 10*time.Minute) {
		t.Fatalf("expected cooldown to expire")
	}
	if shouldSuppressVersion(state, "1.2.4", 24*time.Hour) {
		t.Fatalf("did not expect suppression for different version")
	}
}

func TestShouldCheckInterval(t *testing.T) {
	now := time.Now().UTC()
	state := State{LastCheckedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	if !shouldCheck(state, time.Hour, false) {
		t.Fatalf("expected interval to require check")
	}
	if shouldCheck(state, 3*time.Hour, false) {
		t.Fatalf("expected interval to skip check")
	}
	if !shouldCheck(state, 3*time.Hour, true) {
		t.Fatalf("expected force to require check")
	}
}
