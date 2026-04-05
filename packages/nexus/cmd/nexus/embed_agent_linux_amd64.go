//go:build linux && amd64

package main

import _ "embed"

// embeddedAgent contains the statically compiled nexus-firecracker-agent binary
// for linux/amd64.  It is embedded at build time so that `nexus setup
// firecracker` can inject the agent into the rootfs without access to the Go
// source tree.
//
// To refresh the embedded binary run:
//
//	go generate ./cmd/nexus
//
//go:generate CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o agent-linux-amd64 ../nexus-firecracker-agent/
//go:embed agent-linux-amd64
var embeddedAgent []byte
