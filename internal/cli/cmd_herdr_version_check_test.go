package cli

import (
	"strings"
	"testing"
)

func TestHerdrVersionCheck(t *testing.T) {
	cases := []struct {
		name        string
		installed   string
		pin         string
		wantOutcome versionCheckOutcome
	}{
		{"same", "v0.1.1", "v0.1.1\n", vcoSame},
		{"same_no_v_prefix", "0.1.1", "v0.1.1", vcoSame},
		{"pin_newer", "v0.1.0", "v0.1.1", vcoPinNewer},
		{"installed_newer", "v0.1.2", "v0.1.1", vcoInstalledNewer},
		{"dev", "0.0.0-dev", "v0.1.1", vcoDev},
		{"dev_with_v", "v0.0.0-dev", "v0.1.1", vcoDev},
		{"unparsable_installed", "not-a-version", "v0.1.1", vcoUnparsable},
		{"unparsable_pin", "v0.1.1", "not-a-version", vcoUnparsable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := herdrCheckVersionSkew(tc.installed, tc.pin)
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome=%d want=%d message=%q", got.Outcome, tc.wantOutcome, got.Message)
			}
			if tc.wantOutcome == vcoPinNewer && !strings.Contains(got.Message, herdrUpdateRemedy) {
				t.Errorf("pin-newer message must contain remedy %q; got %q", herdrUpdateRemedy, got.Message)
			}
		})
	}
}

func TestHerdrVersionCheck_DevMutationPinned(t *testing.T) {
	got := herdrCheckVersionSkew("0.0.0-dev", "v0.1.1")
	if got.Outcome != vcoDev {
		t.Errorf("dev build must return vcoDev (exit 12); got outcome %d — remove the -dev check to see this fail", got.Outcome)
	}
}

func TestHerdrVersionCheck_PinNewerContainsRemedy(t *testing.T) {
	got := herdrCheckVersionSkew("v0.1.0", "v0.1.1")
	if !strings.Contains(got.Message, herdrUpdateRemedy) {
		t.Errorf("pin-newer message must contain remedy %q; got %q", herdrUpdateRemedy, got.Message)
	}
}
