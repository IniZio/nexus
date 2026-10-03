//go:build !linux

package hubclient

import "errors"

var DefaultAgentComms = []string{"claude", "codex"}

var ProcRoot = "/proc"

func FindAgentAncestor(start int, comms []string) (int, string, error) {
	return 0, "", errors.ErrUnsupported
}
