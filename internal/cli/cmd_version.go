package cli

import (
	"context"
	"flag"
	"fmt"
	"runtime"

	"github.com/IniZio/nexus/internal/core/hostbin"
)

// version is the build version string. It is overridden at link time via:
//
//	go build -ldflags "-X github.com/IniZio/nexus/internal/cli.version=1.2.3"
var version = "0.0.0-dev"

func init() {
	Register(Command{
		Name:    "version",
		Summary: "Print version and build information",
		Run:     runVersion,
	})
}

func runVersion(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}

	type versionData struct {
		Version             string `json:"version"`
		GoVersion           string `json:"go_version"`
		EmbeddedAgentSHA256 string `json:"embedded_agent_sha256"`
	}
	agentSHA := hostbin.EmbeddedAgentSHA256()
	agentLabel := "none"
	if len(agentSHA) >= 12 {
		agentLabel = agentSHA[:12]
	}
	data := versionData{
		Version:             version,
		GoVersion:           runtime.Version(),
		EmbeddedAgentSHA256: agentSHA,
	}

	out.EmitSuccess("version", data,
		fmt.Sprintf("nexus %s (%s) agent=%s", data.Version, data.GoVersion, agentLabel))
	return nil
}
