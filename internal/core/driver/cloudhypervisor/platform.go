package cloudhypervisor

import "errors"

// errUnsupportedPlatform is returned by the non-Linux stubs of the local
// VM-boot leaf functions. The nexus3 client on a non-Linux host never boots a
// VM locally; it drives a Linux host via `nexus3 orca ... --remote`.
var errUnsupportedPlatform = errors.New("cloudhypervisor: local VM boot is not supported on this platform (host-only); use --remote to boot on a Linux nexus3 host")
