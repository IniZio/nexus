package process

import (
	"strings"
	"testing"
)

func TestStrictSeatbeltProfile_BlocksHostDeveloperTooling(t *testing.T) {
	t.Parallel()

	requiredDenies := []string{
		`(deny process-exec (literal "/usr/bin/xcodebuild"))`,
		`(deny process-exec (literal "/usr/bin/xctest"))`,
		`(deny process-exec (literal "/usr/local/bin/docker"))`,
		`(deny process-exec (literal "/opt/homebrew/bin/docker"))`,
	}

	for _, rule := range requiredDenies {
		if !strings.Contains(strictSeatbeltProfile, rule) {
			t.Fatalf("strict seatbelt profile missing rule: %s", rule)
		}
	}

	if strings.Contains(strictSeatbeltProfile, "(allow network-outbound (remote unix-socket))") {
		t.Fatalf("strict seatbelt profile must not allow unix-socket outbound")
	}
}

func TestRelaxedSeatbeltProfile_AllowsDefault(t *testing.T) {
	t.Parallel()

	if !strings.Contains(relaxedSeatbeltProfile, "(allow default)") {
		t.Fatalf("expected relaxed profile to allow default operations")
	}
}
