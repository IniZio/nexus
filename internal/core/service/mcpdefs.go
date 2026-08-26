package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// SharedMCPServers is the sanitized, guest-safe MCP configuration derived from
// the host agent config, ready to (a) merge into the guest's ~/.claude.json
// mcpServers, (b) feed the MITM secret path, and (c) seed stdio env creds.
type SharedMCPServers struct {
	// Servers is the sanitized mcpServers map (name -> raw JSON entry) to
	// node-merge into the guest ~/.claude.json. http/sse LITERAL credential
	// values have been redacted to synthetic ${VAR} refs; stdio literals kept.
	Servers map[string]json.RawMessage
	// HTTPBinds are SecretBinds for http/sse credentials (both pre-existing
	// ${VAR} refs and the synthetic vars minted for redacted literals) so the
	// MITM proxy swaps the real value at the egress edge. Caller merges these
	// into the sandbox Secrets slice and adds .Hosts to AllowedHosts.
	HTTPBinds []SecretBind
	// StdioEnv is newline-delimited KEY=VALUE lines for stdio ${VAR} creds,
	// appended to the guest cred.env (D-PP-04 stdio relaxation).
	StdioEnv []byte
}

// buildMCPOAuthBindsFn is the injectable implementation used inside
// BuildSharedMCPServers. Tests override it to supply synthetic OAuth binds
// without touching the filesystem.
var buildMCPOAuthBindsFn = BuildMCPOAuthBinds

// BuildSharedMCPServers reads the agent's MCP definitions from the authoritative
// sources (~/.claude.json top-level mcpServers UNIONed with ~/.claude/.mcp.json
// for back-compat; ~/.claude.json wins on name collision) and returns the
// sanitized SharedMCPServers. Gated on profile.MCPConfigFormat==claude-json;
// returns a zero value (all-nil) and nil error when the format is other or no
// sources exist. Malformed/absent source files are a silent skip, never an error.
func BuildSharedMCPServers(profile cred.AgentProfile) (SharedMCPServers, error) {
	if profile.MCPConfigFormat != cred.MCPConfigFormatClaudeJSON {
		return SharedMCPServers{}, nil
	}

	// .mcp.json is back-compat / lower priority.
	byName := make(map[string]json.RawMessage)
	if entries, err := readRawMCPFile(claudeMCPConfigPath(profile)); err == nil {
		for k, v := range entries {
			byName[k] = v
		}
	}
	// ~/.claude.json is authoritative; wins on name collision.
	if entries, err := readRawMCPFile(claudeDotJSONPath(profile)); err == nil {
		for k, v := range entries {
			byName[k] = v
		}
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	result := SharedMCPServers{
		Servers: make(map[string]json.RawMessage),
	}
	stdioVars := make(map[string]string) // var name -> resolved value

	for _, name := range names {
		rawBytes := byName[name]
		var entry rawMCPEntry
		if err := json.Unmarshal(rawBytes, &entry); err != nil {
			// Malformed entry — skip silently.
			continue
		}
		srv, err := buildMCPServer(name, entry)
		if err != nil {
			// Unknown type — skip silently; malformed entries are not fatal.
			continue
		}

		switch srv.Transport {
		case MCPTransportStdio:
			// Pass the ORIGINAL raw bytes verbatim so absent fields (env, args)
			// remain absent in the guest — no null/empty junk from re-marshal.
			result.Servers[name] = rawBytes
			for _, varName := range srv.CredVarRefs {
				if _, ok := stdioVars[varName]; ok {
					continue
				}
				if val := os.Getenv(varName); val != "" {
					stdioVars[varName] = val
				}
			}

		case MCPTransportHTTP, MCPTransportSSE:
			sanitized, binds, err := sanitizeHTTPEntry(name, entry, srv.Host)
			if err != nil {
				return SharedMCPServers{}, fmt.Errorf("mcp defs: sanitize %q: %w", name, err)
			}
			result.Servers[name] = sanitized
			result.HTTPBinds = append(result.HTTPBinds, binds...)
		}
	}

	if len(stdioVars) > 0 {
		keys := make([]string, 0, len(stdioVars))
		for k := range stdioVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		for _, k := range keys {
			fmt.Fprintf(&buf, "%s=%s\n", k, stdioVars[k])
		}
		result.StdioEnv = buf.Bytes()
	}

	// Integrate OAuth MCP binds (same ClaudeJSON gate already checked above).
	credPath := claudeCredentialsPath(profile)
	oauthBinds, _, _ := buildMCPOAuthBindsFn(credPath)

	// Index existing HTTPBinds by Env name to de-dupe.
	existingEnvs := make(map[string]struct{}, len(result.HTTPBinds))
	for _, b := range result.HTTPBinds {
		existingEnvs[b.Env] = struct{}{}
	}

	for _, ob := range oauthBinds {
		// 1. Register the SecretBind (allowlist + MITM injection) unless already present.
		if _, seen := existingEnvs[ob.Bind.Env]; !seen {
			result.HTTPBinds = append(result.HTTPBinds, ob.Bind)
			existingEnvs[ob.Bind.Env] = struct{}{}
		}

		placeholder := "${" + ob.Bind.Env + "}"

		// 2. Inject/merge the Authorization header placeholder into the guest Servers entry.
		if existing, ok := result.Servers[ob.ServerName]; ok {
			// Server exists — re-parse, inject header, re-encode. Skip stdio entries.
			var e rawMCPEntry
			if err := json.Unmarshal(existing, &e); err == nil && e.Command == "" {
				if e.Headers == nil {
					e.Headers = make(map[string]string)
				}
				e.Headers[ob.Header] = placeholder
				if raw, err := json.Marshal(e); err == nil {
					result.Servers[ob.ServerName] = raw
				}
			}
		} else {
			// Server absent from config — synthesize a minimal http guest entry.
			host := ""
			if len(ob.Bind.Hosts) > 0 {
				host = ob.Bind.Hosts[0]
			}
			e := rawMCPEntry{
				Type: "http",
				URL:  "https://" + host,
				Headers: map[string]string{
					ob.Header: placeholder,
				},
			}
			if raw, err := json.Marshal(e); err == nil {
				result.Servers[ob.ServerName] = raw
			}
		}
	}

	return result, nil
}

// readRawMCPFile opens path and decodes the mcpServers map, preserving each
// entry as verbatim json.RawMessage bytes. Absent or malformed files return a
// non-nil error; callers that want a silent-skip should ignore the error.
func readRawMCPFile(path string) (map[string]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var raw struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.NewDecoder(f).Decode(&raw); err != nil {
		return nil, err
	}
	return raw.MCPServers, nil
}

// sanitizeHTTPEntry returns the re-encoded JSON entry with credential-bearing
// literal header values redacted to synthetic ${VAR} refs, and the
// corresponding SecretBinds. Headers are processed in sorted name order for
// determinism.
func sanitizeHTTPEntry(serverName string, entry rawMCPEntry, host string) (json.RawMessage, []SecretBind, error) {
	sanitizedHeaders := make(map[string]string, len(entry.Headers))
	var binds []SecretBind

	hnames := make([]string, 0, len(entry.Headers))
	for k := range entry.Headers {
		hnames = append(hnames, k)
	}
	sort.Strings(hnames)

	for _, hname := range hnames {
		hval := entry.Headers[hname]
		switch {
		case varRefRe.MatchString(hval):
			// Pre-existing ${VAR} refs: keep verbatim, one bind per ref.
			sanitizedHeaders[hname] = hval
			for _, ref := range ParseMCPVarRefs(hval) {
				binds = append(binds, SecretBind{
					Env:   ref.Name,
					Hosts: []string{host},
					Token: os.Getenv(ref.Name),
				})
			}
		case isCredentialHeader(hname, hval):
			// Literal credential: redact to synthetic env var.
			synVar := syntheticMCPVar(serverName, hname)
			sanitizedHeaders[hname] = "${" + synVar + "}"
			binds = append(binds, SecretBind{
				Env:   synVar,
				Hosts: []string{host},
				Token: hval,
			})
		default:
			sanitizedHeaders[hname] = hval
		}
	}

	// Collect url ${VAR} refs (url left verbatim per v1 spec).
	for _, ref := range ParseMCPVarRefs(entry.URL) {
		binds = append(binds, SecretBind{
			Env:   ref.Name,
			Hosts: []string{host},
			Token: os.Getenv(ref.Name),
		})
	}

	out := rawMCPEntry{
		Type:    entry.Type,
		URL:     entry.URL,
		Headers: sanitizedHeaders,
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return raw, binds, nil
}

// isCredentialHeader reports whether a header name or value looks like it
// carries a credential. Matches Authorization by name; "key", "token",
// "secret", or "auth" substrings in the name (case-insensitive); or a
// bearer/sk- prefix in the value.
func isCredentialHeader(name, value string) bool {
	nameLower := strings.ToLower(name)
	if nameLower == "authorization" {
		return true
	}
	for _, kw := range []string{"key", "token", "secret", "auth"} {
		if strings.Contains(nameLower, kw) {
			return true
		}
	}
	valueLower := strings.ToLower(value)
	return strings.HasPrefix(valueLower, "bearer ") || strings.HasPrefix(value, "sk-")
}

// syntheticMCPVar returns the synthetic env var name for a redacted literal
// header value: NEXUS3_MCP_<SERVER>_<HEADER>, uppercased with non-alnum
// chars mapped to underscore.
func syntheticMCPVar(serverName, headerName string) string {
	sanitize := func(s string) string {
		s = strings.ToUpper(s)
		var b strings.Builder
		for _, r := range s {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			} else {
				b.WriteRune('_')
			}
		}
		return b.String()
	}
	return "NEXUS3_MCP_" + sanitize(serverName) + "_" + sanitize(headerName)
}
