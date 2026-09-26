package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCoreBinaryExcludesControllerDeps guards decision D7: Slack, SQLite, tsnet,
// and internal/controller must never be linked into the core nexus binary.
func TestCoreBinaryExcludesControllerDeps(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := exec.LookPath(goBin); err != nil {
		var lerr error
		goBin, lerr = exec.LookPath("go")
		if lerr != nil {
			t.Skip("no go binary found")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, goBin, "list", "-deps", ".")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}

	banned := []string{
		"github.com/slack-go/",
		"modernc.org/sqlite",
		"tailscale.com",
		"github.com/IniZio/nexus/internal/controller",
	}

	for _, pkg := range strings.Fields(string(out)) {
		for _, prefix := range banned {
			p := prefix
			if !strings.HasSuffix(p, "/") {
				p = p + "/"
			}
			if pkg == prefix || strings.HasPrefix(pkg, p) {
				t.Errorf("banned package linked into core binary: %s (matched rule %q)", pkg, prefix)
			}
		}
	}
}
