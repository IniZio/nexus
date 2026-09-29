package service

import (
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/driver/fake"
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

func TestCreateAndBoot_RejectsNetModeEnv(t *testing.T) {
	t.Setenv("NEXUS_NET_MODE", "tap")
	svc := newTestSvc(t, fake.New())
	_, err := CreateAndBoot(context.Background(), svc, nil, fakeDriverFactory(fake.New()), noopProbe, "proj", "nm", CreateAndBootOptions{})
	if err == nil || !strings.Contains(err.Error(), "unset NEXUS_NET_MODE") {
		t.Fatalf("CreateAndBoot err = %v, want net-mode tombstone", err)
	}
}
