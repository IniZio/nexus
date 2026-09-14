package clientagent

import (
	"fmt"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

// Request describes a single port-forward to establish on behalf of a remote client.
type Request struct {
	LocalPort  uint16
	RemotePort uint16
}

func MasterArgv(target, controlPath string) []string {
	return portfwd.MasterArgv(target, controlPath)
}

func ExecArgv(target, controlPath, command string) []string {
	return []string{"ssh", "-S", controlPath, "-o", "BatchMode=yes", target, command}
}

func ForwardArgv(target, controlPath, spec string) []string {
	return []string{"ssh", "-S", controlPath, "-O", "forward", "-L", spec, target}
}

func CancelArgv(target, controlPath, spec string) []string {
	return []string{"ssh", "-S", controlPath, "-O", "cancel", "-L", spec, target}
}

func ForwardSpec(r Request) string {
	return fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", r.LocalPort, r.RemotePort)
}
