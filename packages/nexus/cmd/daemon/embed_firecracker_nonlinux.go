//go:build !linux

package main

// embeddedFirecracker is nil on non-Linux platforms (macOS uses Lima).
var embeddedFirecracker []byte
