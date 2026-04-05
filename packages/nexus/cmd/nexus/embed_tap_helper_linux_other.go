//go:build linux && !amd64 && !arm64

package main

// embeddedTapHelper is nil on uncommon Linux architectures.
// nexus setup firecracker will build nexus-tap-helper from source in this case.
var embeddedTapHelper []byte
