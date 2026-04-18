//go:build linux && !amd64 && !arm64

package main

// embeddedFirecracker is nil on uncommon Linux architectures.
var embeddedFirecracker []byte
