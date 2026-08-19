package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// putBlob stores content under ref and returns the digest it landed on.
func putBlob(t *testing.T, c *Cache, ref, content string) domain.Digest {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	d := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
	img := domain.Image{Digest: d, Ref: ref, Kind: domain.KindBase}
	if err := c.Put(context.Background(), img, bytes.NewReader([]byte(content))); err != nil {
		t.Fatalf("Put(%q): %v", ref, err)
	}
	return d
}

// refHolders returns the digests currently carrying ref.
func refHolders(t *testing.T, c *Cache, ref string) []domain.Digest {
	t.Helper()
	imgs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []domain.Digest
	for _, img := range imgs {
		if img.Ref == ref {
			out = append(out, img.Digest)
		}
	}
	return out
}

// A ref names exactly one image: storing new content under a ref another entry
// already holds must move the ref, not add a second holder. This is the defect
// behind TBD-PD-36 — three entries all tagged nexus3-agent-base, so `--image
// nexus3-agent-base` resolved to whichever the cache scan reached first.
func TestPut_RefTransfersToNewestHolder(t *testing.T) {
	c, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	old := putBlob(t, c, "agent-base", "first build")
	newer := putBlob(t, c, "agent-base", "second build")

	holders := refHolders(t, c, "agent-base")
	if len(holders) != 1 {
		t.Fatalf("ref held by %d entries, want exactly 1: %v", len(holders), holders)
	}
	if holders[0] != newer {
		t.Errorf("ref holder = %s, want the newest entry %s", holders[0], newer)
	}

	// The untagged entry keeps its content and stays reachable by digest —
	// untagging must not be a disguised delete.
	got, err := c.Get(context.Background(), old)
	if err != nil {
		t.Fatalf("Get(untagged %s): %v", old, err)
	}
	if got.Ref != "" {
		t.Errorf("untagged entry Ref = %q, want empty", got.Ref)
	}
	if got.Size != int64(len("first build")) {
		t.Errorf("untagged entry Size = %d, want %d", got.Size, len("first build"))
	}
}

// Untagging must be scoped to the ref being claimed: an unrelated ref on
// another entry is none of this Put's business.
func TestPut_LeavesOtherRefsAlone(t *testing.T) {
	c, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	other := putBlob(t, c, "some-other-base", "unrelated")
	putBlob(t, c, "agent-base", "first build")
	putBlob(t, c, "agent-base", "second build")

	holders := refHolders(t, c, "some-other-base")
	if len(holders) != 1 || holders[0] != other {
		t.Errorf("unrelated ref holders = %v, want [%s]", holders, other)
	}
}

// An empty ref is not a name, so untagged entries must not untag each other.
func TestPut_EmptyRefDoesNotUntag(t *testing.T) {
	c, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	putBlob(t, c, "", "anon one")
	putBlob(t, c, "", "anon two")
	tagged := putBlob(t, c, "agent-base", "tagged")

	imgs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(imgs) != 3 {
		t.Fatalf("cache holds %d entries, want 3", len(imgs))
	}
	holders := refHolders(t, c, "agent-base")
	if len(holders) != 1 || holders[0] != tagged {
		t.Errorf("agent-base holders = %v, want [%s]", holders, tagged)
	}
}
