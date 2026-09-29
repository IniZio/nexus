//go:build !linux

// Package backends links the platform's driver backends and hosts their
// re-exec child dispatch.
package backends

// MaybeRunChild is a no-op on platforms without re-exec children.
func MaybeRunChild() {}
