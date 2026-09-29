//go:build !linux

package supervisor

import "github.com/IniZio/nexus/internal/core/oomattr"

// exitSignal returns the zero Signal off Linux: only cloud-hypervisor records
// how the VM process exited.
func exitSignal(any, string) oomattr.Signal { return oomattr.Signal{} }
