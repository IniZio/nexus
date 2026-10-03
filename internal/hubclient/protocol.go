// Package hubclient is the session-hub client: journald events plus flock-file
// state. It must not import sqlite.
package hubclient

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
	// EnvSeat names the env var holding the caller's seat.
	EnvSeat = "NEXUS_HUB_SEAT"
	// EnvDelivery names the env var selecting the delivery mode.
	EnvDelivery = "NEXUS_HUB_DELIVERY"
)

// TopicHost is the topic for host-level events (binary.installed).
const TopicHost = "host"

// SandboxTopic returns the topic for a sandbox's events.
func SandboxTopic(id string) string { return "sandbox:" + id }

// SeatTopic returns the topic for a seat's direct events.
func SeatTopic(seat string) string { return "seat:" + seat }
