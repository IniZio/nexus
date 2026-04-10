//go:build linux && !amd64 && !arm64

package main

// embeddedAgent is nil on uncommon Linux architectures.
// firecracker bootstrap will build nexus-firecracker-agent from source in this case.
var embeddedAgent []byte
