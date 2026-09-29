//go:build !linux

package cloudhypervisor

// ProbeTapCached is a no-op off Linux: there is no tap perimeter to probe.
func ProbeTapCached(string) error { return nil }

func runTapProbeChild() {}

const netnsProbeEnv = "NEXUS_NETNS_TAP_PROBE"
