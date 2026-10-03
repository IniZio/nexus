package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func goListDeps(t *testing.T, goBin, pkgPath string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "list", "-deps", pkgPath)
	cmd.Env = append(cmd.Environ(), "GOOS=linux")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkgPath, err)
	}
	return strings.Fields(string(out))
}

// TestHostArtifactsLinkedOnlyIntoHost guards that the embedded host-artifact
// package is present in cmd/nexus and absent from cmd/nexus-agent.
func TestHostArtifactsLinkedOnlyIntoHost(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := exec.LookPath(goBin); err != nil {
		var lerr error
		goBin, lerr = exec.LookPath("go")
		if lerr != nil {
			t.Skip("no go binary found")
		}
	}

	const embeddedPkg = "github.com/IniZio/nexus/internal/core/hostbin/embedded"

	hostDeps := goListDeps(t, goBin, ".")
	found := false
	for _, p := range hostDeps {
		if p == embeddedPkg {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cmd/nexus does not link %s", embeddedPkg)
	}

	agentDeps := goListDeps(t, goBin, "../nexus-agent")
	for _, p := range agentDeps {
		if p == embeddedPkg {
			t.Errorf("cmd/nexus-agent must not link %s", embeddedPkg)
		}
	}
}

// TestCoreBinaryExcludesControllerDeps guards decision D7: Slack, SQLite, tsnet,
// internal/controller, and internal/hub must never be linked into the core nexus binary.
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
		"github.com/IniZio/nexus/internal/hub",
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

// TestHubBinaryExcludesEmbeddedArtifacts guards that cmd/nexus-hub does not link
// the embedded artifact blobs (it is itself embedded in them; linking would
// recurse). cmd/nexus-hub is created by H0-HUBBIN; until it exists this test
// skips, and it becomes enforcing as soon as the package appears.
func TestHubBinaryExcludesEmbeddedArtifacts(t *testing.T) {
	if _, err := os.Stat("../nexus-hub"); err != nil {
		t.Skip("cmd/nexus-hub not present yet (created by H0-HUBBIN)")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go binary found")
	}
	const embeddedPkg = "github.com/IniZio/nexus/internal/core/hostbin/embedded"
	for _, p := range goListDeps(t, goBin, "../nexus-hub") {
		if p == embeddedPkg {
			t.Errorf("cmd/nexus-hub must not link %s", embeddedPkg)
		}
	}
}
