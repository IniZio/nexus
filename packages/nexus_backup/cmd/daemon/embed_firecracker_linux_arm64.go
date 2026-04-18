//go:build linux && arm64

package main

import _ "embed"

// embeddedFirecracker contains the statically compiled firecracker binary
// for linux/arm64.
//
//go:generate ./scripts/download-firecracker.sh arm64 firecracker-linux-arm64
//go:embed firecracker-linux-arm64
var embeddedFirecracker []byte
