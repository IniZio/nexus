package image_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/image"
)

// TestAgentTagRoundTrip verifies that AgentTag is persisted via Put and
// returned by List.
func TestAgentTagRoundTrip(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()

	content := []byte("agent-tag-test-content")
	d := digestOf(content)
	wantTag := image.BuilderAgentTag([]byte("some-agent-binary"))

	img := domain.Image{
		Digest:    d,
		Ref:       "debian:tag-test",
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		AgentTag:  wantTag,
	}
	if err := c.Put(ctx, img, bytes.NewReader(content)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	imgs, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *domain.Image
	for i := range imgs {
		if imgs[i].Digest == d {
			found = &imgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("entry not found after Put")
	}
	if found.AgentTag != wantTag {
		t.Errorf("AgentTag = %q, want %q", found.AgentTag, wantTag)
	}
}

// TestAgentTagSurvivesReleaseRefFrom verifies that AgentTag is preserved on
// the entry that LOSES its ref during a releaseRefFrom (i.e. when a new
// image stores the same ref, the old entry keeps its tag but loses its name).
func TestAgentTagSurvivesReleaseRefFrom(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()

	const ref = "debian:survives-test"
	agentTagA := image.BuilderAgentTag([]byte("agent-a"))
	agentTagB := image.BuilderAgentTag([]byte("agent-b"))

	// Store image A with the ref and a tag.
	contentA := []byte("content-a")
	dA := digestOf(contentA)
	imgA := domain.Image{
		Digest:    dA,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		AgentTag:  agentTagA,
	}
	if err := c.Put(ctx, imgA, bytes.NewReader(contentA)); err != nil {
		t.Fatalf("Put A: %v", err)
	}

	// Store image B with the SAME ref — this triggers releaseRefFrom, stripping
	// the ref from A.
	contentB := []byte("content-b")
	dB := digestOf(contentB)
	imgB := domain.Image{
		Digest:    dB,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		AgentTag:  agentTagB,
	}
	if err := c.Put(ctx, imgB, bytes.NewReader(contentB)); err != nil {
		t.Fatalf("Put B: %v", err)
	}

	imgs, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var foundA, foundB *domain.Image
	for i := range imgs {
		switch imgs[i].Digest {
		case dA:
			foundA = &imgs[i]
		case dB:
			foundB = &imgs[i]
		}
	}

	if foundA == nil || foundB == nil {
		t.Fatal("one or both entries missing after Put B")
	}

	// A lost its ref but must keep its AgentTag.
	if foundA.Ref != "" {
		t.Errorf("entry A ref = %q, want empty after releaseRefFrom", foundA.Ref)
	}
	if foundA.AgentTag != agentTagA {
		t.Errorf("entry A AgentTag = %q, want %q after releaseRefFrom", foundA.AgentTag, agentTagA)
	}

	// B keeps both ref and tag.
	if foundB.Ref != ref {
		t.Errorf("entry B ref = %q, want %q", foundB.Ref, ref)
	}
	if foundB.AgentTag != agentTagB {
		t.Errorf("entry B AgentTag = %q, want %q", foundB.AgentTag, agentTagB)
	}
}
