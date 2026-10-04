package herdragent

import "strings"

// GuestShellFallbackMarker is the prefix written to stderr by nexus-guest-shell
// when it falls back to a host shell. Consumers match this prefix to detect a
// broken or host-side pane.
const GuestShellFallbackMarker = "nexus-guest-shell: FALLBACK host shell:"

// GuestShellRefusedMarker is written instead when the guest shell refuses to
// open a host shell for a sandbox-bound pane.
const GuestShellRefusedMarker = "nexus-guest-shell: REFUSED host shell:"

// HostShellMarked reports whether pane output shows a host fallback or refusal.
func HostShellMarked(out string) bool {
	return strings.Contains(out, GuestShellFallbackMarker) || strings.Contains(out, GuestShellRefusedMarker)
}
