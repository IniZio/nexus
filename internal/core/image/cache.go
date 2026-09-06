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
	"syscall"
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

	// pinMu guards pins. pins holds one refcounted flock per digest this
	// process has written; see Pin.
	pinMu sync.Mutex
	pins  map[domain.Digest]*pinEntry
}

// pinEntry is a held lease plus its in-process reference count. flock locks
// attach to the open file description, so two independent opens in the SAME
// process conflict with each other. Refcounting one shared descriptor is what
// lets Put and an outer caller pin the same digest without self-deadlock.
//
// inflight counts Put calls currently streaming into the entry directory. It
// separates the two states a pin can guard:
//
//   - inflight > 0 — the directory is being written; nobody may remove it.
//   - inflight == 0 — the entry is committed but not yet referenced by any
//     sandbox record; no OTHER process may remove it, but this process may,
//     because a caller pruning here knows its own bookkeeping.
type pinEntry struct {
	lease    *entryLease
	n        int
	inflight int
}

// NewCache creates or opens a Cache rooted at root.
// The root directory is created if it does not exist.
func NewCache(root string) (*Cache, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("image cache: init root %s: %w", root, err)
	}
	return &Cache{root: root, pins: make(map[domain.Digest]*pinEntry)}, nil
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

// leaseDir is the directory holding one lock file per digest. It lives beside
// sha256/ rather than inside an entry directory: Prune removes entry
// directories wholesale, so a lease stored inside one would be destroyed by
// the very removal it exists to prevent.
func (c *Cache) leasePath(d domain.Digest) string {
	return filepath.Join(c.root, "locks", d.Algo(), d.Hex()+".lock")
}

// entryLease is a held flock on a digest's lease file.
type entryLease struct {
	f *os.File
}

func (l *entryLease) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// Pin protects d from Prune — in this process and in every other — until the
// returned release function is called.
//
// # Why a pin outlives Put
//
// An image is unreferenced from the moment its bytes start streaming until
// something records it: a sandbox record, or the caller's own pin list. Prune
// computes its keep-set from sandbox records plus explicitly pinned extras, so
// a freshly written image belonging to a DIFFERENT process is, correctly by
// that computation, garbage — and was collected. Two windows were observed on
// three concurrent `nexus3 create --file` builds:
//
//	put:   rename artifact-<n>.tmp → artifact: no such file or directory
//	       (the entry dir was removed mid-stream)
//	boot:  resolve image: digest sha256:…: image cache: not found
//	       (the committed artifact was removed before the sandbox record
//	        referencing it existed)
//
// Put therefore takes a pin before creating the entry directory and KEEPS it:
// releasing at the end of Put would close the first window and leave the
// second. The pin is released when the process exits, which is the point at
// which the sandbox record either exists or never will.
//
// Pin blocks until the lease is available. Blocking is safe: Prune never
// blocks on a lease (it probes with LOCK_NB and resolves a held lease to
// KEEP), so the fact that Put later takes c.mu inside releaseRefFrom while
// Prune takes c.mu before probing cannot deadlock.
func (c *Cache) Pin(d domain.Digest) (func(), error) {
	if !d.Valid() {
		return nil, fmt.Errorf("image cache: pin: invalid digest %q", d)
	}
	c.pinMu.Lock()
	if c.pins == nil {
		c.pins = make(map[domain.Digest]*pinEntry)
	}
	if p, ok := c.pins[d]; ok {
		p.n++
		c.pinMu.Unlock()
		return c.releaseFunc(d), nil
	}
	c.pinMu.Unlock()

	// Acquire outside pinMu: flock can block, and holding pinMu across it
	// would stall unrelated digests.
	lease, err := c.acquireLease(d)
	if err != nil {
		return nil, err
	}

	c.pinMu.Lock()
	defer c.pinMu.Unlock()
	if p, ok := c.pins[d]; ok {
		// Another goroutine won the race; drop our duplicate descriptor.
		lease.release()
		p.n++
		return c.releaseFunc(d), nil
	}
	c.pins[d] = &pinEntry{lease: lease, n: 1}
	return c.releaseFunc(d), nil
}

// markInflight adjusts the in-flight stream count for d by delta.
func (c *Cache) markInflight(d domain.Digest, delta int) {
	c.pinMu.Lock()
	defer c.pinMu.Unlock()
	if p, ok := c.pins[d]; ok {
		p.inflight += delta
	}
}

// ownedPin reports whether this Cache holds a pin for d and, if so, whether a
// Put is currently streaming into it.
func (c *Cache) ownedPin(d domain.Digest) (owned, inflight bool) {
	c.pinMu.Lock()
	defer c.pinMu.Unlock()
	p, ok := c.pins[d]
	if !ok {
		return false, false
	}
	return true, p.inflight > 0
}

// Close releases every pin this Cache holds. A process that exits does this
// implicitly when the kernel closes its descriptors; Close exists for
// long-lived processes and tests that must hand their images over to a
// subsequent Prune.
//
// After Close the Cache remains usable — a later Put simply takes a fresh pin.
func (c *Cache) Close() error {
	c.pinMu.Lock()
	defer c.pinMu.Unlock()
	for d, p := range c.pins {
		p.lease.release()
		delete(c.pins, d)
	}
	return nil
}

// releaseFunc returns an idempotent releaser for one reference on d's pin.
func (c *Cache) releaseFunc(d domain.Digest) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.pinMu.Lock()
			defer c.pinMu.Unlock()
			p, ok := c.pins[d]
			if !ok {
				return
			}
			p.n--
			if p.n <= 0 {
				p.lease.release()
				delete(c.pins, d)
			}
		})
	}
}

// acquireLease blocks until it holds the exclusive in-flight lease for d.
func (c *Cache) acquireLease(d domain.Digest) (*entryLease, error) {
	path := c.leasePath(d)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("image cache: lease: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("image cache: lease: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("image cache: lease: flock %s: %w", path, err)
	}
	return &entryLease{f: f}, nil
}

// tryAcquireLease attempts a non-blocking acquisition of d's lease.
//
// It returns (nil, nil) when the lease is currently held by another writer —
// the caller must treat that as "in flight, KEEP". A missing lease file means
// no writer has ever leased this digest (a pre-lease entry, or one whose lease
// was cleaned up), which is free.
func (c *Cache) tryAcquireLease(d domain.Digest) (*entryLease, error) {
	path := c.leasePath(d)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Lease directory absent: no writer has ever leased a digest here.
			return &entryLease{}, nil
		}
		return nil, fmt.Errorf("image cache: lease: open %s: %w", path, err)
	}
	if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		_ = f.Close()
		if errors.Is(flockErr, syscall.EWOULDBLOCK) || errors.Is(flockErr, syscall.EAGAIN) {
			return nil, nil // held by an in-flight Put
		}
		// Unexpected flock error: cannot verify state. Ambiguity resolves to KEEP.
		return nil, nil
	}
	return &entryLease{f: f}, nil
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

	// Pin BEFORE creating the entry directory, and keep the pin after a
	// successful Put. Between MkdirAll and the final rename the entry carries
	// neither artifact nor meta.json, and after the rename it stays
	// unreferenced until the caller records it — a concurrent Prune would
	// collect it in either window. See Pin for the two observed failures.
	unpin, err := c.Pin(img.Digest)
	if err != nil {
		return err
	}
	c.markInflight(img.Digest, +1)
	committed := false
	defer func() {
		c.markInflight(img.Digest, -1)
		// Release only on failure: nothing was committed, so the pin would
		// otherwise protect an empty directory for the life of the process.
		if !committed {
			unpin()
		}
	}()

	// Artifact already present means this digest is fully committed — by an
	// earlier call or by another process while we waited for the pin. Keep the
	// pin: the caller is about to use this digest and must not race a
	// concurrent Prune to it.
	if _, err := os.Stat(c.artifactPath(img.Digest)); err == nil {
		committed = true
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
	success = true   // artifact is durable; meta failure is recoverable
	committed = true // keep the pin: the entry exists but nothing references it yet

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
// calls in THIS process do not race on directory enumeration and removal.
// That mutex says nothing about other processes, and concurrent `nexus3
// create --file` builds are separate processes: cross-process exclusion is
// the per-digest lease, probed below.
//
// # In-flight entries are never collected
//
// An entry being written by a concurrent Put has neither artifact nor
// meta.json yet, and nothing can reference an image that does not exist, so it
// is always a prune candidate by the referenced-set test alone. Before
// removing a candidate, Prune probes its lease non-blockingly; a held lease
// means a Put is streaming into that directory and the entry is KEPT.
// Without this, one build's post-build GC deleted another build's entry
// directory out from under its rename, and the losing build failed with
// "put: rename artifact: … no such file or directory".
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
		// Decide whether a writer owns this entry.
		//
		// ORDER MATTERS: ownedPin must be consulted BEFORE tryAcquireLease.
		// flock attaches to the open file description, so this process probing
		// its own pin through a second descriptor conflicts with itself — the
		// probe would report "held by someone else" for every digest this
		// Cache wrote, and a blocking variant would deadlock outright.
		var lease *entryLease
		if owned, inflight := c.ownedPin(d); owned {
			// This Cache pinned it. A Put still streaming must not be
			// disturbed; a committed one may be collected, because a caller
			// pruning in this process knows what it wrote.
			if inflight {
				continue
			}
		} else {
			// Not ours: probe the lease. Held (lease == nil) means another
			// process is writing or has written and not yet recorded it.
			// Ambiguity resolves to KEEP.
			var leaseErr error
			lease, leaseErr = c.tryAcquireLease(d)
			if leaseErr != nil {
				return removed, leaseErr
			}
			if lease == nil {
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(algoDir, e.Name())); err != nil {
			lease.release()
			return removed, fmt.Errorf("image cache: prune: remove %s: %w", d, err)
		}
		// DO NOT unlink the lease file here, however tidy it looks.
		//
		// flock protects an INODE, not a path. A writer parked in
		// Flock(LOCK_EX) has already opened inode I; unlinking the path does
		// not wake it, and it goes on to hold the lock on the now-anonymous
		// inode. The next tryAcquireLease O_CREATEs a DIFFERENT inode, takes
		// an uncontended lock on it, classifies the entry as free, and
		// RemoveAll's a directory that a live Put is streaming into — exactly
		// the bug this lease exists to prevent, reintroduced by the cleanup.
		//
		// Lease files are empty and there is at most one per digest ever
		// written on this host, so they cost inodes and nothing else. Leaving
		// them is the correct steady state, not a leak.
		lease.release()
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
