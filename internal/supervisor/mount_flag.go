// mount_flag.go — the SINGLE SOURCE OF TRUTH for encoding a domain.LiveMount
// as a `nexus __supervisor --mount` argument and decoding it back.
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

	"github.com/IniZio/nexus/internal/core/domain"
)

func EncodeLiveMount(lm domain.LiveMount) string {
	spec := lm.HostPath + ":" + lm.GuestPath
	if lm.ReadOnly && !lm.IsFile {
		spec += ":ro"
	} else if lm.ReadOnly && lm.IsFile {
		spec += ":ro:file"
	} else if !lm.ReadOnly && lm.IsFile {
		spec += ":rw:file"
	}
	return spec
}

func ParseLiveMountSpec(spec string) (domain.LiveMount, error) {
	parts := strings.SplitN(spec, ":", 4)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return domain.LiveMount{}, fmt.Errorf("supervisor: --mount %q: want <host-path>:<guest-path>[:ro|:rw][:file]", spec)
	}
	lm := domain.LiveMount{HostPath: parts[0], GuestPath: parts[1]}
	for _, opt := range parts[2:] {
		switch opt {
		case "ro":
			lm.ReadOnly = true
		case "rw":
			// default, no-op
		case "file":
			lm.IsFile = true
		default:
			return domain.LiveMount{}, fmt.Errorf("supervisor: --mount %q: unknown option %q", spec, opt)
		}
	}
	return lm, nil
}
