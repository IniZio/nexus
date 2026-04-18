//go:build linux && arm64 && !embedtaphelper

package main

// embeddedTapHelper is nil when the linux/arm64 helper is not embedded.
// setup falls back to building nexus-tap-helper from source at runtime.
var embeddedTapHelper []byte
