// Package image provides the global, digest-pinned content-addressed image cache.
//
// # On-disk layout
//
//	<root>/
//	  sha256/
//	    <64-hex-chars>/
//	      artifact      — raw rootfs bytes (ext4 image or similar)
//	      meta.json     — JSON-encoded imageRecord (schema-versioned)
//
// One directory per digest ensures that one entry's corruption never prevents
// reading others. The algo-level subdirectory (sha256/) leaves room for future
// hash algorithm agility without a migration.
//
// # Atomicity and durability
//
// Put streams content into a temporary file in the same directory while
// computing its SHA-256. Once the stream is exhausted, the computed digest is
// compared against the declared one. A mismatch (corrupted or truncated source)
// causes the temp file to be removed and Put to return ErrDigestMismatch — no
// partial entry is ever committed under the digest path.
//
// Both the artifact and meta.json are made visible via atomic rename: readers
// see either the complete file or nothing.
//
// # Eviction policy
//
// There is NO automatic eviction. Entries persist indefinitely until an
// explicit Prune call. Prune removes every entry whose digest is NOT in the
// caller-supplied referenced set. This is the only removal mechanism.
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// currentMetaVersion is the schema version written into meta.json.
// A meta.json with a higher version was written by a newer nexus3 binary and
// must not be decoded — partially understanding a record is worse than refusing it.
const currentMetaVersion = 1

// imageRecord is the on-disk JSON representation of image metadata.
// It is intentionally separate from domain.Image so that:
//  1. Only confirmed durable fields are persisted — a future domain field cannot
//     silently land on disk just because it was added to the struct.
//  2. A schema version can be stored without polluting the domain type.
//  3. The encoding contract is explicit and reviewable in one place.
//
// Durable fields (exactly these, nothing more):
//   - identity:   Digest
//   - annotation: Ref, Kind
//   - sizing:     Size
//   - provenance: CreatedAt
type imageRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Digest        domain.Digest    `json:"digest"`
	Ref           string           `json:"ref"`
	Kind          domain.ImageKind `json:"kind"`
	Size          int64            `json:"size"`
	CreatedAt     time.Time        `json:"created_at"`
}

func (r imageRecord) toDomain() domain.Image {
	return domain.Image{
		Digest:    r.Digest,
		Ref:       r.Ref,
		Kind:      r.Kind,
		Size:      r.Size,
		CreatedAt: r.CreatedAt,
	}
}

// ErrNotFound is returned by Get and Open when the digest is not in the cache.
var ErrNotFound = errors.New("image cache: not found")

// ErrDigestMismatch is returned by Put when the content streamed through
// the reader hashes to a different digest than img.Digest declares.
// This indicates a corrupted or truncated source stream.
type ErrDigestMismatch struct {
	Declared domain.Digest
	Computed domain.Digest
}

func (e *ErrDigestMismatch) Error() string {
	return fmt.Sprintf("image cache: digest mismatch: declared %s, computed %s",
		e.Declared, e.Computed)
}

// Cache is a global, digest-pinned content-addressed image cache backed by a
// directory on disk. It is safe for concurrent use from multiple goroutines.
//
// The zero value is not usable; construct one with NewCache.
type Cache struct {
	root string
	mu   sync.Mutex // serialises Prune to prevent concurrent scans from racing on removal
}

// NewCache creates or opens a Cache rooted at root.
// The root directory is created if it does not exist.
func NewCache(root string) (*Cache, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("image cache: init root %s: %w", root, err)
	}
	return &Cache{root: root}, nil
}

func (c *Cache) entryDir(d domain.Digest) string {
	return filepath.Join(c.root, d.Algo(), d.Hex())
}

func (c *Cache) artifactPath(d domain.Digest) string {
	return filepath.Join(c.entryDir(d), "artifact")
}

func (c *Cache) metaPath(d domain.Digest) string {
	return filepath.Join(c.entryDir(d), "meta.json")
}

// Put stores the content from r as the artifact for img.Digest.
//
// The content is streamed into a temporary file while its SHA-256 is computed.
// If the computed digest does not match img.Digest, the temporary file is
// removed and ErrDigestMismatch is returned — no partial or corrupted entry is
// ever committed under the digest path.
//
// If the artifact for this digest already exists in the cache, Put is a no-op
// and returns nil. Metadata is not updated on a no-op Put.
func (c *Cache) Put(ctx context.Context, img domain.Image, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !img.Digest.Valid() {
		return fmt.Errorf("image cache: put: invalid digest %q", img.Digest)
	}
	if !img.Kind.Valid() {
		return fmt.Errorf("image cache: put: invalid kind %v", img.Kind)
	}

	// Fast path: artifact already present means this digest is fully committed.
	if _, err := os.Stat(c.artifactPath(img.Digest)); err == nil {
		return nil
	}

	dir := c.entryDir(img.Digest)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("image cache: put: mkdir %s: %w", dir, err)
	}

	// Stream into a temp file in the entry directory, hashing as we go.
	tmp, err := os.CreateTemp(dir, "artifact-*.tmp")
	if err != nil {
		return fmt.Errorf("image cache: put: create temp: %w", err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("image cache: put: stream: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("image cache: put: fsync artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("image cache: put: close artifact: %w", err)
	}

	// Verify digest before committing.
	computed := domain.Digest("sha256:" + hex.EncodeToString(h.Sum(nil)))
	if computed != img.Digest {
		return &ErrDigestMismatch{Declared: img.Digest, Computed: computed}
	}

	// Rename artifact into place — atomic in the VFS.
	if err := os.Rename(tmpName, c.artifactPath(img.Digest)); err != nil {
		return fmt.Errorf("image cache: put: rename artifact: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("image cache: put: %w", err)
	}
	success = true // artifact is durable; meta failure is recoverable

	createdAt := img.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	rec := imageRecord{
		SchemaVersion: currentMetaVersion,
		Digest:        img.Digest,
		Ref:           img.Ref,
		Kind:          img.Kind,
		Size:          n,
		CreatedAt:     createdAt,
	}
	if err := writeMeta(c.metaPath(img.Digest), rec); err != nil {
		return fmt.Errorf("image cache: put: write meta: %w", err)
	}

	// A ref names exactly one image. Storing a new artifact under a ref that
	// another entry already carries transfers the ref to the new entry, as
	// docker does when a build reuses a tag. Without this, a ref accumulates
	// holders and ref lookup silently picks an arbitrary one — the older
	// entry keeps its content and stays reachable by digest, it just loses
	// the name.
	if err := c.releaseRefFrom(img.Ref, img.Digest); err != nil {
		return fmt.Errorf("image cache: put: %w", err)
	}
	return nil
}

// releaseRefFrom clears ref from every cache entry except keep. An empty ref
// is a no-op: untagged entries do not compete for a name.
//
// A failure to rewrite one entry's meta.json aborts: leaving the ref on two
// entries is exactly the ambiguity this exists to prevent, so a partial
// release must be reported rather than swallowed.
func (c *Cache) releaseRefFrom(ref string, keep domain.Digest) error {
	if ref == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	imgs, err := c.List(context.Background())
	if err != nil {
		return fmt.Errorf("release ref %q: %w", ref, err)
	}
	for _, img := range imgs {
		if img.Ref != ref || img.Digest == keep {
			continue
		}
		rec := imageRecord{
			SchemaVersion: currentMetaVersion,
			Digest:        img.Digest,
			Ref:           "",
			Kind:          img.Kind,
			Size:          img.Size,
			CreatedAt:     img.CreatedAt,
		}
		if err := writeMeta(c.metaPath(img.Digest), rec); err != nil {
			return fmt.Errorf("release ref %q from %s: %w", ref, img.Digest, err)
		}
	}
	return nil
}

// Get returns the Image metadata for the given digest.
// Returns ErrNotFound if the digest is not in the cache.
func (c *Cache) Get(_ context.Context, d domain.Digest) (domain.Image, error) {
	if !d.Valid() {
		return domain.Image{}, fmt.Errorf("image cache: get: invalid digest %q", d)
	}
	if _, err := os.Stat(c.artifactPath(d)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.Image{}, fmt.Errorf("%w: %s", ErrNotFound, d)
		}
		return domain.Image{}, fmt.Errorf("image cache: get: stat artifact: %w", err)
	}
	rec, err := readMeta(c.metaPath(d))
	if err != nil {
		return domain.Image{}, fmt.Errorf("image cache: get %s: %w", d, err)
	}
	return rec.toDomain(), nil
}

// Open returns a ReadCloser for the raw artifact bytes of the given digest.
// Returns ErrNotFound if the digest is not in the cache.
// The caller is responsible for closing the returned reader.
func (c *Cache) Open(_ context.Context, d domain.Digest) (io.ReadCloser, error) {
	if !d.Valid() {
		return nil, fmt.Errorf("image cache: open: invalid digest %q", d)
	}
	f, err := os.Open(c.artifactPath(d))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, d)
		}
		return nil, fmt.Errorf("image cache: open %s: %w", d, err)
	}
	return f, nil
}

// List returns metadata for all cache entries this binary can decode.
// Entries with missing, unreadable, or future-version meta.json are silently
// skipped — one bad entry must not prevent the CLI from listing the rest.
// An error is returned only if the cache root cannot be read.
func (c *Cache) List(_ context.Context) ([]domain.Image, error) {
	algoDir := filepath.Join(c.root, "sha256")
	entries, err := os.ReadDir(algoDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("image cache: list: read %s: %w", algoDir, err)
	}

	var out []domain.Image
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := domain.Digest("sha256:" + e.Name())
		if !d.Valid() {
			continue // skip stray non-digest-shaped directories
		}
		rec, err := readMeta(filepath.Join(algoDir, e.Name(), "meta.json"))
		if err != nil {
			continue // skip unreadable or future-version entries
		}
		out = append(out, rec.toDomain())
	}
	return out, nil
}

// Prune removes all cache entries whose digest is NOT in referenced.
// It returns the number of entries removed. Referenced digests that are not
// present in the cache are silently ignored.
//
// No automatic eviction occurs outside of an explicit Prune call.
//
// Prune holds an internal mutex for its duration so that concurrent Prune
// calls do not race on directory enumeration and removal.
func (c *Cache) Prune(_ context.Context, referenced []domain.Digest) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	refSet := make(map[domain.Digest]struct{}, len(referenced))
	for _, d := range referenced {
		refSet[d] = struct{}{}
	}

	algoDir := filepath.Join(c.root, "sha256")
	entries, err := os.ReadDir(algoDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("image cache: prune: read %s: %w", algoDir, err)
	}

	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := domain.Digest("sha256:" + e.Name())
		if !d.Valid() {
			continue
		}
		if _, keep := refSet[d]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(algoDir, e.Name())); err != nil {
			return removed, fmt.Errorf("image cache: prune: remove %s: %w", d, err)
		}
		removed++
	}
	return removed, nil
}

// writeMeta atomically persists rec to path via temp-file + fsync + rename +
// directory fsync. Mirrors the durability guarantees of store.writeRecord.
func writeMeta(path string, rec imageRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("image cache: marshal meta: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "meta-*.tmp")
	if err != nil {
		return fmt.Errorf("image cache: create temp meta in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("image cache: write temp meta: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("image cache: fsync temp meta: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("image cache: close temp meta: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("image cache: rename meta to %s: %w", path, err)
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	success = true
	return nil
}

// readMeta reads and decodes meta.json from path.
// Returns ErrNotFound if the file is absent.
// Returns an error if the file's schema version exceeds currentMetaVersion.
func readMeta(path string) (imageRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return imageRecord{}, fmt.Errorf("%w: meta.json missing at %s", ErrNotFound, path)
		}
		return imageRecord{}, fmt.Errorf("image cache: read meta %s: %w", path, err)
	}
	var rec imageRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return imageRecord{}, fmt.Errorf("image cache: decode meta %s: %w", path, err)
	}
	if rec.SchemaVersion > currentMetaVersion {
		return imageRecord{}, fmt.Errorf(
			"image cache: meta version %d > supported %d at %s; upgrade nexus3",
			rec.SchemaVersion, currentMetaVersion, path)
	}
	return rec, nil
}

// syncDir opens dir and calls Sync() to commit updated directory entries
// (e.g. a newly renamed file) to stable storage.
//
// This is required after rename(2) to guarantee durability across a power cut:
// without a directory fsync, the rename may be silently lost on reboot even
// though the file data was synced.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("image cache: open dir %s for fsync: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("image cache: fsync dir %s: %w", dir, err)
	}
	return d.Close()
}
