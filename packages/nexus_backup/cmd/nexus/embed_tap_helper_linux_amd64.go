//go:build linux && amd64

package main

import _ "embed"

// embeddedTapHelper contains the statically compiled nexus-tap-helper binary
// for linux/amd64.  It is embedded at build time so that `nexus setup
// firecracker` works without access to the Go source tree.
//
// To refresh the embedded binary run:
//
//	go generate ./cmd/nexus
//
//go:generate go build -trimpath -ldflags="-s -w" -o tap-helper-linux-amd64 ../nexus-tap-helper/
//go:embed tap-helper-linux-amd64
var embeddedTapHelper []byte
