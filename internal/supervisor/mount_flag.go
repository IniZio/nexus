// mount_flag.go — the SINGLE SOURCE OF TRUTH for encoding a domain.LiveMount
// as a `nexus3 __supervisor --mount` argument and decoding it back.
//
// The supervisor is spawned as a detached process whose entire configuration
// travels as argv (see BuildSupervisorArgv / parseSupervisorFlags). Encoding
// and decoding MUST go through this pair: deriving the spec format
// independently on either side reintroduces the silent-drift class that cost
// this repo a dead-backend virtiofs boot (a supervisor booted the VM with
// fs=null and memory.shared=false while the guest cmdline still asked to mount
// the virtiofs tag, so the guest agent blocked forever at mount time).
package supervisor

import (
	"fmt"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// EncodeLiveMount renders lm as "<host-path>:<guest-path>[:ro]", the same spec
// shape the user types for `nexus3 create --mount`.
func EncodeLiveMount(lm domain.LiveMount) string {
	spec := lm.HostPath + ":" + lm.GuestPath
	if lm.ReadOnly {
		spec += ":ro"
	}
	return spec
}

// ParseLiveMountSpec is the inverse of EncodeLiveMount.
//
// It deliberately does NOT stat the host path: the path was validated when the
// sandbox was created, and the supervisor may start long afterwards. Only the
// spec's shape is checked.
func ParseLiveMountSpec(spec string) (domain.LiveMount, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return domain.LiveMount{}, fmt.Errorf("supervisor: --mount %q: want <host-path>:<guest-path>[:ro]", spec)
	}
	lm := domain.LiveMount{HostPath: parts[0], GuestPath: parts[1]}
	if len(parts) == 3 {
		if parts[2] != "ro" {
			return domain.LiveMount{}, fmt.Errorf("supervisor: --mount %q: unknown option %q; want <host-path>:<guest-path>[:ro]", spec, parts[2])
		}
		lm.ReadOnly = true
	}
	return lm, nil
}
