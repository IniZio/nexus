package service

// TBD-PD-32: the sandbox record must remember which agent it was created for.
//
// domain.Sandbox.AgentName is inert unless CreateAndBoot writes it, and no
// existing test reads it — a field added to the struct and to the filestore
// mapping but never populated would look fully wired and do nothing. These
// tests drive the real CreateAndBoot path and assert on the returned record.

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

func TestCreateAndBoot_RecordsAgentName(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	broker := cred.NewBroker()
	capture := &captureSeeder{}

	svc := newTestSvc(t, fake.New())
	var opts CreateAndBootOptions
	opts.Image = ImageSpec{Digest: string(img.Digest)}
	opts.CacheRoot = cacheRoot
	opts.DiskDir = t.TempDir()
	WireClaudeEgress(&opts, broker, capture.fn(), nil)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "agent-named", opts)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if sb.AgentName != cred.ClaudeCodeProfileName {
		t.Errorf("AgentName = %q, want %q — the sandbox cannot be re-seeded for the right agent on restart without it",
			sb.AgentName, cred.ClaudeCodeProfileName)
	}
}

// A sandbox created without an agent must record no agent. An empty AgentName
// is what tells the restart path there is no credential seed to deliver, so a
// mapping that defaulted to "claude-code" would silently promise credentials
// to a plain sandbox.
func TestCreateAndBoot_NoAgent_RecordsEmptyAgentName(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	svc := newTestSvc(t, fake.New())
	var opts CreateAndBootOptions
	opts.Image = ImageSpec{Digest: string(img.Digest)}
	opts.CacheRoot = cacheRoot
	opts.DiskDir = t.TempDir()

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "plain", opts)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if sb.AgentName != "" {
		t.Errorf("AgentName = %q, want empty for a sandbox created with no agent", sb.AgentName)
	}
}
