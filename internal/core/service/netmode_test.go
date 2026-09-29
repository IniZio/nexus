package service

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestCheckNetModeEnv(t *testing.T) {
	const want = "NEXUS_NET_MODE is no longer supported (tap networking was removed in S9d; vhost-user is the only mode): unset NEXUS_NET_MODE"
	for _, v := range []string{"tap", "vhost-user", "bogus"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("NEXUS_NET_MODE", v)
			err := CheckNetModeEnv()
			if err == nil || err.Error() != want {
				t.Fatalf("CheckNetModeEnv() = %v, want %q", err, want)
			}
		})
	}
	t.Run("unset", func(t *testing.T) {
		t.Setenv("NEXUS_NET_MODE", "")
		if err := CheckNetModeEnv(); err != nil {
			t.Fatalf("CheckNetModeEnv() = %v, want nil", err)
		}
	})
}

func TestLegacyEmptyNetModeStaysTap(t *testing.T) {
	// Empty persisted net_mode is judged by the tap NIC identity, not vhost.
	legacy := domain.Sandbox{GuestTapName: "tap0", VhostSocket: "/run/x.sock"}
	if !legacy.HasNICIdentity() {
		t.Error("empty net_mode with a tap name must be adoptable as tap")
	}
	if (domain.Sandbox{VhostSocket: "/run/x.sock"}).HasNICIdentity() {
		t.Error("empty net_mode must not treat a vhost socket as its NIC identity")
	}
}
