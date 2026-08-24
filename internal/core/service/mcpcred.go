package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// claudeMCPConfigPath returns the path to the Claude Code MCP config file.
// When profile.ConfigDirEnvVar is set and the corresponding env var is
// non-empty, that directory is used; otherwise ~/.claude is the default.
// The file name is always ".mcp.json".
func claudeMCPConfigPath(profile cred.AgentProfile) string {
	dir := ""
	if profile.ConfigDirEnvVar != "" {
		dir = os.Getenv(profile.ConfigDirEnvVar)
	}
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".mcp.json")
}

// resolveMCPStdioPayload reads the agent's MCP config and resolves ${VAR}
// credential references for stdio servers from the host environment. It returns
// KEY=VALUE lines ready to append to the guest credential seed payload.
//
// # D-PP-04 exemption (stdio MCP only)
//
// Stdio MCP servers are child processes launched BY the in-guest agent; they
// inherit environment variables directly from the agent process. There is no
// network path the MITM proxy can intercept, so credential brokering is
// structurally impossible for the stdio transport.
//
// This function therefore injects the resolved variable values as PLAINTEXT
// into the guest cred.env file. This is a deliberate, scoped relaxation of
// the zero-cred-in-guest invariant (D-PP-04), limited strictly to stdio MCP
// credential variables. HTTP/SSE servers remain brokered (C-SECRET /
// ResolveMCPHTTPBinds). Claude's own OAuth token is unaffected.
//
// Gate: only active when profile.MCPConfigFormat == MCPConfigFormatClaudeJSON
// and the config file exists. Absent or malformed config is a silent no-op.
func resolveMCPStdioPayload(profile cred.AgentProfile) []byte {
	if profile.MCPConfigFormat != cred.MCPConfigFormatClaudeJSON {
		return nil
	}
	servers, err := ParseMCPConfigFile(claudeMCPConfigPath(profile))
	if err != nil {
		// File absent is an expected no-op; parse errors are also silent so a
		// malformed MCP config does not break sandbox creation.
		return nil
	}

	var buf bytes.Buffer
	seen := make(map[string]struct{})
	for _, srv := range servers {
		if srv.Transport != MCPTransportStdio {
			continue
		}
		for _, name := range srv.CredVarRefs {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			val := os.Getenv(name)
			if val == "" {
				continue // unset on host → omit; not an error
			}
			fmt.Fprintf(&buf, "%s=%s\n", name, val)
		}
	}
	return buf.Bytes()
}

// ResolveMCPHTTPBinds reads the agent's MCP config and derives a SecretBind
// for each ${VAR} credential reference found in http/sse server headers or
// URLs. The returned binds feed into the sandbox Secrets slice so the MITM
// proxy swaps the real credential at the egress edge (C-SECRET).
//
// Gate: only active when profile.MCPConfigFormat == MCPConfigFormatClaudeJSON
// and the config file exists. Absent config returns nil, nil (no-op).
//
// Each bind covers exactly one host (derived from the server's URL). Callers
// must also add bind.Hosts to the sandbox AllowedHosts so agent sandboxes
// can reach the MCP server through the closed-egress ACL.
func ResolveMCPHTTPBinds(profile cred.AgentProfile) ([]SecretBind, error) {
	if profile.MCPConfigFormat != cred.MCPConfigFormatClaudeJSON {
		return nil, nil
	}
	servers, err := ParseMCPConfigFile(claudeMCPConfigPath(profile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("mcp http binds: parse config: %w", err)
	}

	var binds []SecretBind
	for _, srv := range servers {
		if srv.Transport != MCPTransportHTTP && srv.Transport != MCPTransportSSE {
			continue
		}
		if srv.Host == "" || len(srv.CredVarRefs) == 0 {
			continue
		}
		for _, name := range srv.CredVarRefs {
			binds = append(binds, SecretBind{
				Env:   name,
				Hosts: []string{srv.Host},
				Token: os.Getenv(name),
			})
		}
	}
	return binds, nil
}
