package service

import (
	"os"
	"path/filepath"
	"regexp"
)

// safeNameRe matches characters that are safe for filesystem paths in a
// per-server store filename. Everything else is replaced with an underscore.
var safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// sanitizeForFS replaces characters unsafe for filesystem use with underscores.
// Returns "_" if the result would be empty.
func sanitizeForFS(name string) string {
	s := safeNameRe.ReplaceAllString(name, "_")
	if s == "" {
		return "_"
	}
	return s
}

// DefaultMCPOAuthStoreRoot returns the default host-side directory where
// per-server MCP OAuth credential stores are persisted.
// Typically ~/.config/nexus/mcp-creds/.
func DefaultMCPOAuthStoreRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "nexus", "mcp-creds")
}
