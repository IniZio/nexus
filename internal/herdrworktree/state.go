package herdrworktree

import (
	"strings"
)

func IsSandboxNotFound(err error, output string) bool {
	combined := strings.ToLower(err.Error() + " " + output)
	return strings.Contains(combined, "not found") || strings.Contains(combined, "no such")
}

// SandboxListed reports whether `nexus sandbox list` still shows handle or sandboxID.
func SandboxListed(psOut, handle, sandboxID string) bool {
	for _, line := range strings.Split(psOut, "\n") {
		for _, tok := range strings.Fields(line) {
			if (handle != "" && tok == handle) || (sandboxID != "" && tok == sandboxID) {
				return true
			}
		}
	}
	return false
}
