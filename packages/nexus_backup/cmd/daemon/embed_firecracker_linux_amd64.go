//go:build linux && amd64

package main

import _ "embed"

// embeddedFirecracker contains the statically compiled firecracker binary
// for linux/amd64. It is embedded at build time so that the daemon can
// auto-install firecracker on first start without requiring a separate download.
//
// To refresh the embedded binary run:
//
//	go generate ./cmd/daemon
//
//go:generate ./scripts/download-firecracker.sh amd64 firecracker-linux-amd64
//go:embed firecracker-linux-amd64
var embeddedFirecracker []byte
