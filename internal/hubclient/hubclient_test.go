package hubclient

import (
	"os/exec"
	"strings"
	"testing"
)

func TestHubclientImportsNeitherHubNorSQLite(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "github.com/IniZio/nexus/internal/hub/") ||
			p == "github.com/IniZio/nexus/internal/hub" ||
			strings.HasPrefix(p, "modernc.org/sqlite") {
			t.Errorf("hubclient must not link %s", p)
		}
	}
}
