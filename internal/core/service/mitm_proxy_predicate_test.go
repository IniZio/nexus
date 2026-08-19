package service

// TBD-PD-32: the component that starts the MITM proxy and the component that
// waits for its CA certificate must agree about whether a proxy exists.
//
// They did not. startSupervisor built a proxy when `!OpenEgress ||
// SecretHosts`, while the supervisor's seed loop retried 30 times at 2s
// intervals waiting for a CA cert — a full minute of delay before READY, on
// every sandbox the perimeter had already decided needed no proxy, after which
// it seeded nothing. SandboxHasMITMProxy is the single predicate both now read.

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

func TestSandboxHasMITMProxy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		sb   domain.Sandbox
		want bool
		why  string
	}{
		{
			name: "curated allowlist",
			sb:   domain.Sandbox{Envelope: domain.Envelope{OpenEgress: false}},
			want: true,
			why:  "the proxy is what enforces the allowlist at L7",
		},
		{
			name: "open egress, nothing to swap",
			sb:   domain.Sandbox{Envelope: domain.Envelope{OpenEgress: true}},
			want: false,
			why:  "build tools should see real server certs when nothing needs swapping",
		},
		{
			name: "open egress with secret hosts",
			sb: domain.Sandbox{Envelope: domain.Envelope{
				OpenEgress:  true,
				SecretHosts: []string{"api.github.com"},
			}},
			want: true,
			why:  "the human/git path needs the placeholder swap",
		},
		{
			name: "open egress with an agent",
			sb: domain.Sandbox{
				Envelope:  domain.Envelope{OpenEgress: true},
				AgentName: cred.ClaudeCodeProfileName,
			},
			want: true,
			why:  "an agent with no proxy holds a placeholder bearer nothing exchanges",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SandboxHasMITMProxy(tc.sb); got != tc.want {
				t.Errorf("SandboxHasMITMProxy = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}
