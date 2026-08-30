// Package handoff defines the wire contract for transferring live file
// descriptors and adoption state from an outgoing per-sandbox supervisor to
// an incoming one, and the SCM_RIGHTS transport that moves them over a Unix
// socket.
//
// A handoff is by construction an old binary talking to a new one, so the
// [Payload] shape is a compatibility surface from day one (motive
// nexus3-host-supervisor-hotswap, TBD-6). [CurrentVersion] is the version
// this binary produces and understands; [Offer] and [Accept] together
// guarantee that a version the receiver does not understand is a resumable
// failure for the sender (D-HSH-08): the outgoing supervisor keeps its
// descriptors and processes until it has a positive [Ack] in hand, so there
// is never a window in which nobody owns the perimeter.
//
// Slice 01's FD-ownership audit (D-HSH-09) found exactly two resources cross
// a swap: the perimeter socketpair fd (R1, [Payload.Perimeter]) and the
// virtiofsd child processes (R7, [Payload.Virtiofs]). R7 is coupled to the
// outgoing supervisor by Pdeathsig, not by a descriptor, so re-adoption is a
// process-survival problem (reparent by pid + re-point by vhost-user socket
// path) rather than an fd transfer — see [VirtiofsHandle]. Three further
// resources carry non-fd state that must be serialised rather than passed as
// descriptors: the MITM CA material ([Payload.CA]), the credential broker's
// placeholder→token map ([Payload.Credentials]), and the governor's boot
// vCPU/memory configuration ([Payload.Governor]).
package handoff

// Version1 is the initial handoff wire version.
const Version1 = 1

// CurrentVersion is the payload version this binary produces when acting as
// the outgoing side, and the version [Accept] accepts when acting as the
// incoming side.
const CurrentVersion = Version1

// Payload is the versioned handoff message sent by the outgoing supervisor
// to the incoming one. It is serialised as JSON and travels as the regular
// (non-ancillary) data of a single SOCK_DGRAM datagram; the perimeter fd
// referenced by [Payload.Perimeter] travels as SCM_RIGHTS ancillary data
// attached to that same datagram (see [Offer] / [Accept]).
type Payload struct {
	// Version identifies the shape of this payload. A receiver that does not
	// recognise Version MUST respond with a refusal [Ack] (OK: false) rather
	// than guessing at the layout — see [Accept].
	Version int `json:"version"`

	// Perimeter describes R1, the perimeter socketpair fd. The fd itself is
	// not a JSON field: it rides as SCM_RIGHTS ancillary data on the same
	// send call. Perimeter.Present tells the receiver whether to expect one.
	Perimeter PerimeterHandle `json:"perimeter"`

	// Virtiofs describes R7, the live virtiofsd child processes, one entry
	// per active live mount. Nil or empty means no live mounts are active.
	Virtiofs []VirtiofsHandle `json:"virtiofs,omitempty"`

	// CA carries the MITM proxy's CA material. Regenerating the CA on
	// handoff would invalidate every certificate the guest has already
	// pinned this boot, so it is carried as state rather than re-derived.
	CA CAMaterial `json:"ca"`

	// Credentials is the credential broker's placeholder→real-token map.
	// Losing it on handoff would strand every credential the guest was
	// already seeded with under a placeholder the new broker no longer
	// recognises.
	Credentials map[string]string `json:"credentials,omitempty"`

	// Governor carries the boot vCPU/memory configuration the auto-resize
	// governor needs to resume against, without re-deriving it from a VM
	// that already booted under the outgoing supervisor.
	Governor GovernorConfig `json:"governor"`
}

// PerimeterHandle describes R1, the perimeter socketpair fd (perimConn).
// The fd itself is out-of-band (SCM_RIGHTS); this struct carries only the
// metadata needed to interpret it.
type PerimeterHandle struct {
	// Present is true when a perimeter fd accompanies this payload's send
	// call. False means the outgoing side had no live perimeter connection
	// to transfer (e.g. the perimeter was never brought up).
	Present bool `json:"present"`
}

// VirtiofsHandle describes one live virtiofsd child process (R7) to be
// re-adopted by the incoming supervisor. Re-adoption is a process-survival
// operation, not an fd transfer: the incoming side must reparent the
// process referenced by PID (defusing the outgoing supervisor's Pdeathsig
// before it can fire) and resume treating SocketPath as the backing
// vhost-user socket for the already-attached CH `fs` device. CH has no API
// to re-point a live device to a different socket path, so SocketPath here
// must match exactly what cloud-hypervisor was given at vm.create.
type VirtiofsHandle struct {
	// PID is the OS process ID of the running virtiofsd child.
	PID int `json:"pid"`

	// SocketPath is the vhost-user socket path virtiofsd is bound to and
	// that cloud-hypervisor's `fs` device was created against.
	SocketPath string `json:"socket_path"`

	// SharedDir is the host directory virtiofsd is exporting.
	SharedDir string `json:"shared_dir"`

	// ReadOnly mirrors the --readonly flag virtiofsd was started with.
	ReadOnly bool `json:"read_only"`
}

// CAMaterial is the MITM proxy's per-sandbox CA certificate and private key,
// PEM-encoded exactly as [perimeter/mitm.Proxy] holds them.
type CAMaterial struct {
	CertPEM []byte `json:"cert_pem,omitempty"`
	KeyPEM  []byte `json:"key_pem,omitempty"`
}

// GovernorConfig is the boot vCPU/memory configuration the auto-resize
// governor was constructed with.
type GovernorConfig struct {
	// VCPUCount is the number of vCPUs the VM was booted with.
	VCPUCount int `json:"vcpu_count"`

	// MemoryMB is the amount of guest RAM, in mebibytes, the VM was booted
	// with.
	MemoryMB uint64 `json:"memory_mb"`
}

// Validate returns a non-empty refusal reason when the payload is missing
// fields that must be populated for a complete handoff. An incomplete handoff
// is worse than no handoff: the replacement inherits some resources but not
// others, producing an inconsistent supervisor. The caller must treat a
// non-empty Validate() result as a clean refusal (ok=false) so the outgoing
// supervisor stays alive and continues to own all resources (D-HSH-08).
//
// Specifically, [Payload.CA] must carry the MITM CA material (CertPEM and
// KeyPEM). A replacement that lacks the CA cannot continue serving TLS
// interception for the sandbox's egress perimeter — it would have to
// regenerate a new CA, which breaks any in-flight TLS session and violates
// the invariant that the perimeter is transparent across a hot-swap.
//
// [Payload.Credentials] and [Payload.Virtiofs] are allowed to be empty: a
// sandbox with no brokered credentials and no live virtiofs mounts produces
// a legitimately nil/empty slice for both.
func (p Payload) Validate() string {
	if len(p.CA.CertPEM) == 0 || len(p.CA.KeyPEM) == 0 {
		return "handoff payload incomplete: CA.CertPEM and CA.KeyPEM must be populated " +
			"(mitm.Proxy CA key export not yet wired — wire it to remove this check)"
	}
	return ""
}

// Ack is the incoming supervisor's response to a [Payload]. The sender MUST
// NOT release or close any resource named in the payload until it has read
// a positive Ack — that is what makes a refusal resumable (D-HSH-08).
type Ack struct {
	// OK is true when the incoming side accepted the payload and has taken
	// ownership of every resource named in it (dup'd the fd, recorded the
	// virtiofsd pids for adoption, etc). False means the incoming side has
	// NOT taken ownership of anything; the outgoing side remains the sole
	// owner and must resume normal operation.
	OK bool `json:"ok"`

	// Reason is a human-readable refusal explanation, set when OK is false.
	Reason string `json:"reason,omitempty"`

	// SupportedVersion is the highest [Payload.Version] the incoming side
	// understands. Set when OK is false due to a version mismatch.
	SupportedVersion int `json:"supported_version,omitempty"`
}
