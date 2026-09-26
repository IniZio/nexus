package service_test

import (
	"os"
	"strings"
	"testing"
)

func TestNoMCPRefresherOutsideVault(t *testing.T) {
	files := []string{
		"mcpoauth_refresh.go",
		"mcpoauth.go",
		"mcpoauth_vault.go",
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "cred.NewRefresher") {
			t.Errorf("%s: found cred.NewRefresher; MCP OAuth must use vault, not file-based refresher", name)
		}
	}
}
