package buildinfo

import "runtime/debug"

// ProtocolVersion is incremented on any breaking change to the daemon's
// WebSocket RPC surface or PTY protocol.  The Mac app checks this value
// against the version reported by its bundled binary to decide whether an
// update is needed.
const ProtocolVersion = 2

var (
	CLIName               = "nexus"
	DaemonName            = "nexus-daemon"
	CLIVersion            = "0.0.0-dev"
	DaemonVersion         = "0.0.0-dev"
	Commit                = ""
	BuiltAt               = ""
	UpdatePublicKeyBase64 = ""
)

type Info struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	BuiltAt  string `json:"builtAt"`
	Protocol int    `json:"protocol"`
}

func CLI() Info {
	version := CLIVersion
	if version == "" || version == "0.0.0-dev" {
		if v := versionFromBuildSettings(); v != "" {
			version = v
		}
	}
	return Info{Name: CLIName, Version: version, Commit: Commit, BuiltAt: BuiltAt, Protocol: ProtocolVersion}
}

func Daemon() Info {
	version := DaemonVersion
	if version == "" || version == "0.0.0-dev" {
		if v := versionFromBuildSettings(); v != "" {
			version = v
		}
	}
	return Info{Name: DaemonName, Version: version, Commit: Commit, BuiltAt: BuiltAt, Protocol: ProtocolVersion}
}

func versionFromBuildSettings() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return ""
}
