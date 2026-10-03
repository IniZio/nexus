// Package hubclient holds the wire contract between the core nexus binary and
// the embedded nexus-hub binary. It must not import internal/hub or sqlite.
package hubclient

// ProtocolVersion is passed as `nexus-hub --protocol N <verb>`. A mismatch makes
// nexus-hub exit ExitProtocolMismatch with a message on stderr.
const ProtocolVersion = 1

// ExitProtocolMismatch is the nexus-hub exit code on a protocol mismatch.
const ExitProtocolMismatch = 3

// Verbs.
const (
	VerbAppend  = "append"   // stdin: one event JSON; stdout: {"seq":n}
	VerbLast    = "last"     // --subject S
	VerbLastAll = "last-all" // last event per subject
	VerbWatch   = "watch"    // --topic T --cursor C [--ack]; JSONL stdout, acks on stdin with --ack
	VerbVersion = "version"
)

// Watch line kinds.
const (
	KindEvent = "event"
	KindGap   = "gap"
)

// Actors.
const (
	ActorAnonymous  = "anonymous"
	ActorSupervisor = "system:supervisor"
	// EnvSession names the env var whose value is the actor for CLI callers.
	EnvSession = "NEXUS_HUB_SESSION"
)

// TopicHost is the topic for host-level events (binary.installed).
const TopicHost = "host"

// SandboxTopic returns the topic for a sandbox's events.
func SandboxTopic(id string) string { return "sandbox:" + id }
