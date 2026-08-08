package image_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// digestOf computes the sha256 digest of content and returns a domain.Digest.
func digestOf(content []byte) domain.Digest {
	h := sha256.Sum256(content)
	return domain.MustDigest("sha256:" + hex.EncodeToString(h[:]))
}

func newCache(t *testing.T) *image.Cache {
	t.Helper()
	c, err := image.NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c
}

func makeImage(content []byte) (domain.Image, io.Reader) {
	d := digestOf(content)
	img := domain.Image{
		Digest:    d,
		Ref:       "nexus3-test:latest",
		Kind:      domain.KindBase,
		Size:      int64(len(content)),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	return img, bytes.NewReader(content)
}

// ── Digest type tests ──────────────────────────────────────────────────────────

// TestDigestRoundTrip verifies that ParseDigest preserves the canonical string
// and that Algo/Hex return the expected components.
func TestDigestRoundTrip(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	canonical := "sha256:" + hex64

	d, err := domain.ParseDigest(canonical)
	if err != nil {
		t.Fatalf("ParseDigest(%q): %v", canonical, err)
	}
	if d.String() != canonical {
		t.Errorf("String() = %q, want %q", d.String(), canonical)
	}
	if d.Algo() != "sha256" {
		t.Errorf("Algo() = %q, want %q", d.Algo(), "sha256")
	}
	if d.Hex() != hex64 {
		t.Errorf("Hex() = %q, want %q", d.Hex(), hex64)
	}
}

// TestDigestValidation verifies the acceptance/rejection boundaries.
func TestDigestValidation(t *testing.T) {
	cases := []struct {
		input string
		valid bool
	}{
		{"sha256:" + strings.Repeat("a", 64), true},
		{"sha256:" + strings.Repeat("0", 64), true},
		{"sha256:" + strings.Repeat("f", 64), true},
		// Empty / wrong prefix
		{"", false},
		{"sha256:", false},
		{"md5:abc", false},
		{":" + strings.Repeat("a", 64), false},
		// Wrong hex length
		{"sha256:" + strings.Repeat("a", 63), false},
		{"sha256:" + strings.Repeat("a", 65), false},
		// Invalid hex characters
		{"sha256:" + strings.Repeat("z", 64), false},
		{"sha256:" + strings.Repeat("g", 64), false},
	}
	for _, tc := range cases {
		_, err := domain.ParseDigest(tc.input)
		if tc.valid && err != nil {
			t.Errorf("ParseDigest(%q): unexpected error: %v", tc.input, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("ParseDigest(%q): expected error, got nil", tc.input)
		}
	}
}

// TestMustDigestPanics verifies that MustDigest panics on invalid input.
func TestMustDigestPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustDigest with invalid input: expected panic, got none")
		}
	}()
	domain.MustDigest("not-a-digest")
}

// TestDigestJSONRoundTrip verifies marshal/unmarshal round-trip.
func TestDigestJSONRoundTrip(t *testing.T) {
	original := domain.MustDigest("sha256:" + strings.Repeat("b", 64))
	img := domain.Image{
		Digest:    original,
		Kind:      domain.KindBuilder,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	// Round-trip through the cache (which uses JSON internally).
	ctx := context.Background()
	c := newCache(t)
	content := []byte("json round trip test")
	// Use the actual content digest, not our arbitrary one, to satisfy Put.
	img2, r := makeImage(content)
	if err := c.Put(ctx, img2, r); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.Get(ctx, img2.Digest)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Digest != img2.Digest {
		t.Errorf("Digest after JSON round-trip: got %s, want %s", got.Digest, img2.Digest)
	}
	_ = img // suppress unused var
}

// ── Cache put / lookup / list / prune tests ───────────────────────────────────

// TestPutGetRoundTrip verifies all metadata fields survive a Put → Get cycle.
func TestPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	content := []byte("hello nexus3 image cache")
	img, r := makeImage(content)

	if err := c.Put(ctx, img, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := c.Get(ctx, img.Digest)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Digest != img.Digest {
		t.Errorf("Digest: got %s, want %s", got.Digest, img.Digest)
	}
	if got.Ref != img.Ref {
		t.Errorf("Ref: got %q, want %q", got.Ref, img.Ref)
	}
	if got.Kind != img.Kind {
		t.Errorf("Kind: got %v, want %v", got.Kind, img.Kind)
	}
	if got.Size != int64(len(content)) {
		t.Errorf("Size: got %d, want %d", got.Size, len(content))
	}
}

// TestPutIdempotent verifies that storing the same digest twice is a no-op.
func TestPutIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	content := []byte("idempotent content")
	img, r := makeImage(content)

	if err := c.Put(ctx, img, r); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := c.Put(ctx, img, bytes.NewReader(content)); err != nil {
		t.Fatalf("second Put: %v", err)
	}
}

// TestPutCorruptedContentRejected verifies that a content stream that produces
// a different digest than declared is rejected, and no entry is committed.
func TestPutCorruptedContentRejected(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	content := []byte("the real bytes")
	img, _ := makeImage(content)

	// Supply bytes that hash differently — simulates a corrupt or truncated stream.
	corrupt := []byte("different bytes")
	err := c.Put(ctx, img, bytes.NewReader(corrupt))
	if err == nil {
		t.Fatal("Put with corrupted content: expected error, got nil")
	}
	var mismatch *image.ErrDigestMismatch
	if !errors.As(err, &mismatch) {
		t.Errorf("expected *ErrDigestMismatch, got %T: %v", err, err)
	}

	// After a rejected Put, the digest must not appear in the cache.
	_, err = c.Get(ctx, img.Digest)
	if !errors.Is(err, image.ErrNotFound) {
		t.Errorf("Get after failed Put: expected ErrNotFound, got %v", err)
	}
}

// TestPutTruncatedContentRejected verifies that a short (truncated) artifact
// is rejected, mirroring the "short artifact is rejected" requirement.
func TestPutTruncatedContentRejected(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	content := []byte("full content that is expected")
	img, _ := makeImage(content)

	truncated := content[:5]
	err := c.Put(ctx, img, bytes.NewReader(truncated))
	if err == nil {
		t.Fatal("Put with truncated content: expected error, got nil")
	}
	var mismatch *image.ErrDigestMismatch
	if !errors.As(err, &mismatch) {
		t.Errorf("expected *ErrDigestMismatch, got %T: %v", err, err)
	}
}

// TestList verifies that List returns all committed entries and no extras.
func TestList(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	inputs := [][]byte{
		[]byte("entry one"),
		[]byte("entry two"),
		[]byte("entry three"),
	}
	var digests []domain.Digest
	for _, content := range inputs {
		img, r := makeImage(content)
		if err := c.Put(ctx, img, r); err != nil {
			t.Fatalf("Put: %v", err)
		}
		digests = append(digests, img.Digest)
	}

	list, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != len(inputs) {
		t.Fatalf("List: got %d entries, want %d", len(list), len(inputs))
	}
	seen := make(map[domain.Digest]bool, len(list))
	for _, img := range list {
		seen[img.Digest] = true
	}
	for _, d := range digests {
		if !seen[d] {
			t.Errorf("List: missing digest %s", d)
		}
	}
}

// TestListEmptyCache verifies that List on an empty cache returns nil, nil.
func TestListEmptyCache(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	list, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List on empty cache: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List on empty cache: got %d entries, want 0", len(list))
	}
}

// TestPruneKeepsReferencedRemovesUnreferenced is the core prune contract test:
// referenced entries survive; unreferenced entries are removed.
func TestPruneKeepsReferencedRemovesUnreferenced(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	keep := [][]byte{[]byte("keep this"), []byte("keep that too")}
	remove := [][]byte{[]byte("remove me"), []byte("and remove me")}

	var keepDigests []domain.Digest
	for _, content := range keep {
		img, r := makeImage(content)
		if err := c.Put(ctx, img, r); err != nil {
			t.Fatalf("Put (keep): %v", err)
		}
		keepDigests = append(keepDigests, img.Digest)
	}

	var removeDigests []domain.Digest
	for _, content := range remove {
		img, r := makeImage(content)
		if err := c.Put(ctx, img, r); err != nil {
			t.Fatalf("Put (remove): %v", err)
		}
		removeDigests = append(removeDigests, img.Digest)
	}

	n, err := c.Prune(ctx, keepDigests)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != len(remove) {
		t.Errorf("Prune: removed %d, want %d", n, len(remove))
	}

	for _, d := range keepDigests {
		if _, err := c.Get(ctx, d); err != nil {
			t.Errorf("Get (kept) %s after prune: %v", d, err)
		}
	}
	for _, d := range removeDigests {
		_, err := c.Get(ctx, d)
		if !errors.Is(err, image.ErrNotFound) {
			t.Errorf("Get (removed) %s after prune: expected ErrNotFound, got %v", d, err)
		}
	}
}

// TestPruneWithNilReferenced verifies that Prune(nil) removes all entries.
func TestPruneWithNilReferenced(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	for _, content := range [][]byte{[]byte("alpha"), []byte("beta")} {
		img, r := makeImage(content)
		if err := c.Put(ctx, img, r); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	n, err := c.Prune(ctx, nil)
	if err != nil {
		t.Fatalf("Prune(nil): %v", err)
	}
	if n != 2 {
		t.Errorf("Prune(nil): removed %d, want 2", n)
	}

	list, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List after full prune: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List after full prune: got %d entries, want 0", len(list))
	}
}

// TestPruneEmptyCache verifies that Prune on an empty cache is a no-op.
func TestPruneEmptyCache(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	n, err := c.Prune(ctx, nil)
	if err != nil {
		t.Fatalf("Prune on empty cache: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune on empty cache: removed %d, want 0", n)
	}
}

// TestOpenArtifact verifies that Open returns the exact bytes stored by Put.
func TestOpenArtifact(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	content := []byte("raw artifact bytes for open test — should come back verbatim")
	img, r := makeImage(content)
	if err := c.Put(ctx, img, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := c.Open(ctx, img.Digest)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll from Open: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("artifact bytes mismatch:\n  got  %q\n  want %q", got, content)
	}
}

// TestGetNotFound verifies that Get for an absent digest returns ErrNotFound.
func TestGetNotFound(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	absent := domain.MustDigest("sha256:" + strings.Repeat("1", 64))
	_, err := c.Get(ctx, absent)
	if !errors.Is(err, image.ErrNotFound) {
		t.Errorf("Get unknown digest: expected ErrNotFound, got %v", err)
	}
}

// TestOpenNotFound verifies that Open for an absent digest returns ErrNotFound.
func TestOpenNotFound(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	absent := domain.MustDigest("sha256:" + strings.Repeat("2", 64))
	_, err := c.Open(ctx, absent)
	if !errors.Is(err, image.ErrNotFound) {
		t.Errorf("Open unknown digest: expected ErrNotFound, got %v", err)
	}
}
