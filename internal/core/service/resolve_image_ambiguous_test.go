package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// seedRef stores content under ref with an explicit creation time, bypassing
// Cache.Put's ref-transfer rule by writing each entry with an empty ref first
// and then re-tagging — this reproduces a cache written before that rule
// existed, which is exactly the state resolveExt4 must refuse.
func seedRef(t *testing.T, c *image.Cache, ref, content string, created time.Time) domain.Digest {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	d := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
	img := domain.Image{Digest: d, Ref: ref, Kind: domain.KindBase, CreatedAt: created}
	if err := c.Put(context.Background(), img, bytes.NewReader([]byte(content))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return d
}

// retag rewrites an entry's meta.json ref field in place. It edits the cache
// on disk rather than going through Put, because Put is precisely the code
// that now prevents two entries sharing a ref — the point of the test is to
// reconstruct a cache that predates that rule.
func retag(t *testing.T, root string, d domain.Digest, ref string) {
	t.Helper()
	path := filepath.Join(root, d.Algo(), d.Hex(), "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read meta %s: %v", path, err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("unmarshal meta %s: %v", path, err)
	}
	rec["ref"] = ref
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatalf("write meta %s: %v", path, err)
	}
}

// TBD-PD-36: three cache entries all tagged nexus3-agent-base meant `--image
// nexus3-agent-base` booted the OLDEST one, silently testing stale code. The
// resolver must refuse rather than pick, and must name the digests so the
// operator can pin one.
func TestResolveExt4_AmbiguousRefIsRefused(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	base := time.Date(2026, 8, 18, 1, 52, 0, 0, time.UTC)
	oldest := seedRef(t, c, "agent-base", "oldest build", base)
	newest := seedRef(t, c, "agent-base", "newest build", base.Add(6*time.Hour))
	// Cache.Put transferred the ref to `newest`; force the pre-rule state back
	// by re-tagging the older entry directly.
	retag(t, root, oldest, "agent-base")

	_, _, err = resolveExt4(context.Background(), ImageSpec{Ref: "agent-base"}, c, root, nil)
	if err == nil {
		t.Fatal("resolveExt4 returned no error for an ambiguous ref; it picked one silently")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous") {
		t.Errorf("error does not say the ref is ambiguous: %s", msg)
	}
	for _, d := range []domain.Digest{oldest, newest} {
		if !strings.Contains(msg, string(d)) {
			t.Errorf("error omits candidate digest %s, so the operator cannot pin one: %s", d, msg)
		}
	}
	// Newest first: the digest they most likely want should lead.
	if strings.Index(msg, string(newest)) > strings.Index(msg, string(oldest)) {
		t.Errorf("candidates are not newest-first: %s", msg)
	}
}

// The unambiguous path must keep working — a single holder still resolves.
func TestResolveExt4_SingleRefStillResolves(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	d := seedRef(t, c, "agent-base", "only build", time.Now().UTC())

	path, digest, err := resolveExt4(context.Background(), ImageSpec{Ref: "agent-base"}, c, root, nil)
	if err != nil {
		t.Fatalf("resolveExt4: %v", err)
	}
	if digest != string(d) {
		t.Errorf("digest = %s, want %s", digest, d)
	}
	if !strings.Contains(path, d.Hex()) {
		t.Errorf("path %q does not point at digest %s", path, d)
	}
}
