package vending

import "testing"

func TestGetEnvVars(t *testing.T) {
	providers := []string{"codex", "opencode"}
	env := GetEnvVars(providers)

	if env["NEXUS_VENDING_URL"] != "http://localhost:10790" {
		t.Error("NEXUS_VENDING_URL not set correctly")
	}

	if env["CODEX_API_URL"] == "" {
		t.Error("CODEX_API_URL not set")
	}

	if env["OPENCODE_API_URL"] == "" {
		t.Error("OPENCODE_API_URL not set")
	}
}
