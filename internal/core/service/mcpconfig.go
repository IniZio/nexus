package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
)

// MCPTransport identifies the wire protocol for an MCP server entry.
type MCPTransport string

const (
	MCPTransportStdio MCPTransport = "stdio"
	MCPTransportHTTP  MCPTransport = "http"
	MCPTransportSSE   MCPTransport = "sse"
)

// MCPVarRef is a single ${VAR} or ${VAR:-default} reference extracted from
// a config string.
type MCPVarRef struct {
	// Name is the environment-variable name (e.g. "API_KEY").
	Name string
	// Default is non-empty when the reference used ${VAR:-default} syntax.
	Default string
}

// MCPServer is the parsed, transport-typed representation of one mcpServers
// entry from a Claude Code MCP config file.
type MCPServer struct {
	// Name is the key from the mcpServers map.
	Name string
	// Transport is stdio, http, or sse.
	Transport MCPTransport
	// URL is set for http and sse servers; empty for stdio.
	URL string
	// Host is derived from URL via net/url; empty for stdio.
	Host string
	// Env holds environment variable overrides for stdio servers.
	Env map[string]string
	// Headers holds HTTP headers for http/sse servers.
	Headers map[string]string
	// CredVarRefs is the deduplicated set of ${VAR} names referenced anywhere
	// in this server's env values, header values, or url. Each element is the
	// variable name only; defaults are accessible via ParseMCPVarRefs.
	CredVarRefs []string
}

// rawMCPEntry is the JSON shape for a single mcpServers entry.
// omitempty is safe here: it only affects Marshal (http/sse re-encode path),
// not Unmarshal — absent fields stay zero-valued on decode regardless.
type rawMCPEntry struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// rawMCPFile is the JSON shape of .mcp.json or the mcpServers section of
// ~/.claude.json.
type rawMCPFile struct {
	MCPServers map[string]rawMCPEntry `json:"mcpServers"`
}

// varRefRe matches ${VAR} and ${VAR:-default}.
var varRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-(.*?))?\}`)

// ParseMCPVarRefs returns all ${VAR} / ${VAR:-default} references found in s.
// Multiple occurrences of the same variable in the same string each produce a
// separate MCPVarRef entry (callers that need dedup should use collectCredVarRefs).
func ParseMCPVarRefs(s string) []MCPVarRef {
	matches := varRefRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	refs := make([]MCPVarRef, 0, len(matches))
	for _, m := range matches {
		refs = append(refs, MCPVarRef{Name: m[1], Default: m[2]})
	}
	return refs
}

// collectCredVarRefs gathers the unique set of variable names referenced in
// the provided strings, returning them sorted for determinism.
func collectCredVarRefs(strs ...string) []string {
	seen := map[string]struct{}{}
	for _, s := range strs {
		for _, ref := range ParseMCPVarRefs(s) {
			seen[ref.Name] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// hostFromURL extracts the hostname from rawURL; returns empty string on error
// or when rawURL is empty.
func hostFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ParseMCPConfig parses MCP server configuration from r (JSON with a top-level
// "mcpServers" key, as produced by Claude Code). It returns one MCPServer per
// entry, in sorted-name order, or an error if the JSON is malformed.
func ParseMCPConfig(r io.Reader) ([]MCPServer, error) {
	var raw rawMCPFile
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("mcpconfig: JSON decode: %w", err)
	}
	if len(raw.MCPServers) == 0 {
		return nil, nil
	}

	// Sort names so output is deterministic.
	names := make([]string, 0, len(raw.MCPServers))
	for n := range raw.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)

	servers := make([]MCPServer, 0, len(names))
	for _, name := range names {
		entry := raw.MCPServers[name]
		srv, err := buildMCPServer(name, entry)
		if err != nil {
			return nil, err
		}
		servers = append(servers, srv)
	}
	return servers, nil
}

// ParseMCPConfigBytes is a convenience wrapper around ParseMCPConfig for
// callers that already have the JSON in memory.
func ParseMCPConfigBytes(data []byte) ([]MCPServer, error) {
	return ParseMCPConfig(newBytesReader(data))
}

// ParseMCPConfigFile reads path from disk and delegates to ParseMCPConfig.
func ParseMCPConfigFile(path string) ([]MCPServer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mcpconfig: open %s: %w", path, err)
	}
	defer f.Close()
	return ParseMCPConfig(f)
}

// buildMCPServer converts a raw JSON entry into a typed MCPServer.
func buildMCPServer(name string, e rawMCPEntry) (MCPServer, error) {
	srv := MCPServer{Name: name}

	switch {
	case e.Command != "" || e.Type == "" || e.Type == "stdio":
		// stdio: command present (or type absent/explicit "stdio").
		srv.Transport = MCPTransportStdio
		srv.Env = e.Env

		// Collect var refs from env values only (command/args are not brokered
		// by the later secret slice, but env is).
		envVals := make([]string, 0, len(e.Env))
		for _, v := range e.Env {
			envVals = append(envVals, v)
		}
		srv.CredVarRefs = collectCredVarRefs(envVals...)

	case e.Type == "http":
		srv.Transport = MCPTransportHTTP
		srv.URL = e.URL
		srv.Host = hostFromURL(e.URL)
		srv.Headers = e.Headers

		headerVals := make([]string, 0, len(e.Headers))
		for _, v := range e.Headers {
			headerVals = append(headerVals, v)
		}
		srv.CredVarRefs = collectCredVarRefs(append(headerVals, e.URL)...)

	case e.Type == "sse":
		srv.Transport = MCPTransportSSE
		srv.URL = e.URL
		srv.Host = hostFromURL(e.URL)
		srv.Headers = e.Headers

		headerVals := make([]string, 0, len(e.Headers))
		for _, v := range e.Headers {
			headerVals = append(headerVals, v)
		}
		srv.CredVarRefs = collectCredVarRefs(append(headerVals, e.URL)...)

	default:
		return MCPServer{}, fmt.Errorf("mcpconfig: server %q: unknown type %q", name, e.Type)
	}

	return srv, nil
}

// newBytesReader wraps a []byte as an io.Reader without importing bytes in the
// call site (bytes is already imported for json internally).
func newBytesReader(b []byte) io.Reader {
	return &bytesReader{b: b}
}

type bytesReader struct {
	b   []byte
	pos int
}

func (r *bytesReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n = copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
