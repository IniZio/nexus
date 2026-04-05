//go:build linux && arm64

package main

import _ "embed"

// embeddedTapHelper contains the statically compiled nexus-tap-helper binary
// for linux/arm64 (e.g. Apple Silicon Lima VMs).
//
//go:generate go build -trimpath -ldflags="-s -w" -GOOS=linux -GOARCH=arm64 -o tap-helper-linux-arm64 ../nexus-tap-helper/
//go:embed tap-helper-linux-arm64
var embeddedTapHelper []byte
