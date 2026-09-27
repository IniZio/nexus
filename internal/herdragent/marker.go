package herdragent

// GuestShellFallbackMarker is the prefix written to stderr by nexus-guest-shell
// when it falls back to a host shell. Consumers match this prefix to detect a
// broken or host-side pane.
const GuestShellFallbackMarker = "nexus-guest-shell: FALLBACK host shell:"
