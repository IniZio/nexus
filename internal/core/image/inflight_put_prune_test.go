package image_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// blockingReader yields the first half of content, signals ready, waits for
// release, then yields the rest. It lets a test hold a Put open at exactly the
// point where the entry directory exists but the artifact has not been renamed
// into place yet.
type blockingReader struct {
	content   []byte
	off       int
	ready     chan struct{}
	release   chan struct{}
	signalled bool
}

func newBlockingReader(content []byte) *blockingReader {
	return &blockingReader{
		content: content,
		ready:   make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if b.off >= len(b.content) {
		return 0, io.EOF
	}
	half := len(b.content) / 2
	if b.off >= half && !b.signalled {
		b.signalled = true
		close(b.ready)
		<-b.release
	}
	end := b.off + len(p)
	if end > len(b.content) {
		end = len(b.content)
	}
	if !b.signalled && end > half {
		end = half
	}
	n := copy(p, b.content[b.off:end])
	b.off += n
	return n, nil
}

// TestPrune_DoesNotDeleteInFlightPut is the regression test for the concurrent
// build harvest failure:
//
//	image cache: put: rename artifact: rename …/<digest>/artifact-<n>.tmp
//	…/<digest>/artifact: no such file or directory
//
// Put creates the entry directory and streams into artifact-*.tmp inside it,
// so between MkdirAll and the final rename the entry carries neither artifact
// nor meta.json. Prune enumerated sha256/* and removed every directory whose
// digest was not in the referenced set — and an in-flight digest can never be
// in that set, because nothing references an image that does not exist yet.
// A concurrent build's post-build GC therefore deleted another build's entry
// directory out from under its rename.
//
// Mutation proof: remove the lease probe from Prune (or the lease acquisition
// from Put) and this test fails with that exact ENOENT rename error.
func TestPrune_DoesNotDeleteInFlightPut(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	ctx := context.Background()

	content := bytes.Repeat([]byte("in-flight-artifact-bytes"), 4096)
	img := domain.Image{
		Digest:    digestOf(content),
		Ref:       "nexus3-inflight:latest",
		Kind:      domain.KindBuilder,
		Size:      int64(len(content)),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	br := newBlockingReader(content)
	var wg sync.WaitGroup
	var putErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		putErr = c.Put(ctx, img, br)
	}()

	// Wait until the Put is mid-stream: the entry dir and its temp file exist,
	// the artifact does not.
	select {
	case <-br.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("Put never reached mid-stream")
	}
	entryDir := filepath.Join(root, img.Digest.Algo(), img.Digest.Hex())
	if _, statErr := os.Stat(entryDir); statErr != nil {
		t.Fatalf("entry dir absent mid-Put: %v", statErr)
	}

	// A concurrent build's post-build GC: nothing references the in-flight
	// digest, so it is a prune candidate.
	removed, pruneErr := c.Prune(ctx, nil)
	if pruneErr != nil {
		t.Fatalf("Prune: %v", pruneErr)
	}
	if removed != 0 {
		t.Errorf("Prune removed %d entr(ies) while a Put was in flight, want 0", removed)
	}
	if _, statErr := os.Stat(entryDir); statErr != nil {
		t.Errorf("Prune deleted the in-flight entry dir %s: %v", entryDir, statErr)
	}

	close(br.release)
	wg.Wait()

	if putErr != nil {
		t.Fatalf("Put failed because a concurrent Prune deleted its entry dir: %v", putErr)
	}
	// The committed artifact must be intact and readable.
	got, err := c.Get(ctx, img.Digest)
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if got.Digest != img.Digest {
		t.Errorf("Get digest = %s, want %s", got.Digest, img.Digest)
	}
	rc, err := c.Open(ctx, img.Digest)
	if err != nil {
		t.Fatalf("Open after Put: %v", err)
	}
	defer rc.Close() //nolint:errcheck
	stored, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Errorf("stored artifact differs from source (%d vs %d bytes)", len(stored), len(content))
	}
}

// TestPrune_DoesNotDeleteCommittedUnrecordedEntry covers the SECOND window of
// the same race, seen live once the first was closed:
//
//	error: sandbox create: service: create-and-boot race/r0: resolve image:
//	digest "sha256:cec3…": image cache: not found
//
// Put had committed the artifact, but between that commit and the sandbox
// record referencing it, a concurrent build's post-build GC collected it —
// that GC pins only its OWN new digest, and nothing else referenced this one
// yet. A pin released at the end of Put closes only the first window.
//
// The writer and the pruner are separate Cache instances over one root, the
// production shape: two `nexus3 create --file` processes.
//
// Mutation proof: release the pin at the end of Put (drop `committed = true`)
// and this test fails — the committed entry is collected before its owner can
// record it.
func TestPrune_DoesNotDeleteCommittedUnrecordedEntry(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	writer, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (writer): %v", err)
	}
	img, reader := makeImage([]byte("committed-but-not-yet-recorded"))
	img.Ref = ""
	if err := writer.Put(ctx, img, reader); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// A concurrent build finishes and runs its post-build GC. It pins only its
	// own digest, so ours is a candidate.
	pruner, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (pruner): %v", err)
	}
	removed, err := pruner.Prune(ctx, nil)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 0 {
		t.Errorf("concurrent Prune removed %d entr(ies) still owned by a live build, want 0", removed)
	}
	if _, err := writer.Get(ctx, img.Digest); err != nil {
		t.Fatalf("the writing build can no longer resolve its own image: %v", err)
	}

	// Once the writing build exits, the entry is ordinary garbage again.
	if err := writer.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}
	removed, err = pruner.Prune(ctx, nil)
	if err != nil {
		t.Fatalf("Prune after writer exit: %v", err)
	}
	if removed != 1 {
		t.Errorf("Prune after writer exit removed %d entries, want 1", removed)
	}
}

// TestPrune_StillRemovesUnreferencedIdleEntries guards the other side of the
// fix: the pin must not turn Prune into a no-op. Entries written by a build
// that has since finished carry no live pin, so an unreferenced one is still
// collected — while its lease file stays behind, which is deliberate.
//
// The writer and the pruner are separate Cache instances over one root — the
// production shape, where the pruning build is a different process from the
// build that wrote the image. writer.Close() stands in for that process
// exiting.
//
// Mutation proof: make Prune skip every entry and this test fails.
func TestPrune_StillRemovesUnreferencedIdleEntries(t *testing.T) {
	root := t.TempDir()
	writer, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (writer): %v", err)
	}
	ctx := context.Background()

	keptContent := []byte("keep-me")
	keptImg, keptReader := makeImage(keptContent)
	if err := writer.Put(ctx, keptImg, keptReader); err != nil {
		t.Fatalf("Put kept: %v", err)
	}

	goneContent := []byte("collect-me")
	goneImg, goneReader := makeImage(goneContent)
	goneImg.Ref = "" // a ref names one image; avoid the release-ref path
	if err := writer.Put(ctx, goneImg, goneReader); err != nil {
		t.Fatalf("Put gone: %v", err)
	}
	// The writing build finishes and its pins go with it.
	if err := writer.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (pruner): %v", err)
	}
	removed, err := c.Prune(ctx, []domain.Digest{keptImg.Digest})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("Prune removed %d entries, want 1", removed)
	}
	if _, err := c.Get(ctx, keptImg.Digest); err != nil {
		t.Errorf("referenced entry was collected: %v", err)
	}
	if _, err := c.Get(ctx, goneImg.Digest); !errors.Is(err, image.ErrNotFound) {
		t.Errorf("unreferenced entry survived Prune: err = %v", err)
	}

	// The collected entry's lease file MUST survive — see
	// TestPrune_LeaseFileOutlivesTheEntryItGuards for why unlinking it breaks
	// mutual exclusion.
	if !hasLeaseFor(t, root, goneImg.Digest) {
		t.Errorf("lease file for %s was unlinked by Prune; flock protects an inode, "+
			"not a path, so recreating it later hands two writers independent locks",
			goneImg.Digest)
	}
}

// TestPrune_LeaseFileOutlivesTheEntryItGuards pins down the lease-file
// lifecycle, which none of the other tests exercise: Prune removes entry
// directories but must NEVER unlink a lease file.
//
// flock protects an inode, not a path. A writer parked in Flock(LOCK_EX) has
// already opened inode I; unlinking the path leaves it holding the lock on an
// anonymous inode, while the next probe O_CREATEs a DIFFERENT inode, locks it
// uncontended, and classifies a directory that is being written as free. The
// assertion is therefore on inode identity, not merely on existence — a lease
// file that is deleted and recreated is a different lock.
//
// Mutation proof: reintroduce `os.Remove(c.leasePath(d))` in Prune and this
// test fails.
func TestPrune_LeaseFileOutlivesTheEntryItGuards(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	writer, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (writer): %v", err)
	}
	img, reader := makeImage([]byte("entry-that-gets-collected"))
	img.Ref = ""
	if err := writer.Put(ctx, img, reader); err != nil {
		t.Fatalf("Put: %v", err)
	}
	leasePath := findLeaseFor(t, root, img.Digest)
	inoBefore := inodeOf(t, leasePath)
	if err := writer.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	pruner, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (pruner): %v", err)
	}
	removed, err := pruner.Prune(ctx, nil)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("Prune removed %d entries, want 1", removed)
	}

	// The lease file, and specifically its inode, must be untouched.
	if _, statErr := os.Stat(leasePath); statErr != nil {
		t.Fatalf("Prune unlinked the lease file %s: %v", leasePath, statErr)
	}
	if inoAfter := inodeOf(t, leasePath); inoAfter != inoBefore {
		t.Fatalf("lease inode changed across Prune: %d → %d; a recreated lease file "+
			"is a different lock and no longer excludes a parked writer",
			inoBefore, inoAfter)
	}

	// A third party probing the same digest must still contend on that inode:
	// a live writer is seen as held, and the entry is KEPT.
	secondWriter, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (second writer): %v", err)
	}
	reader2 := bytes.NewReader([]byte("entry-that-gets-collected"))
	if err := secondWriter.Put(ctx, img, reader2); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if inoNow := inodeOf(t, leasePath); inoNow != inoBefore {
		t.Errorf("second writer leased a different inode (%d, want %d)", inoNow, inoBefore)
	}

	prober, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache (prober): %v", err)
	}
	removed, err = prober.Prune(ctx, nil)
	if err != nil {
		t.Fatalf("third-party Prune: %v", err)
	}
	if removed != 0 {
		t.Errorf("third-party Prune removed %d entr(ies) held by a live writer, want 0", removed)
	}
	if _, err := secondWriter.Get(ctx, img.Digest); err != nil {
		t.Errorf("live writer lost its image to a third-party Prune: %v", err)
	}
}

// findLeaseFor returns the lease path for d, failing if absent.
func findLeaseFor(t *testing.T, root string, d domain.Digest) string {
	t.Helper()
	for _, p := range leaseFiles(t, root) {
		if strings.Contains(p, d.Hex()) {
			return p
		}
	}
	t.Fatalf("no lease file for %s under %s", d, root)
	return ""
}

// hasLeaseFor reports whether a lease file for d exists under root.
func hasLeaseFor(t *testing.T, root string, d domain.Digest) bool {
	t.Helper()
	for _, p := range leaseFiles(t, root) {
		if strings.Contains(p, d.Hex()) {
			return true
		}
	}
	return false
}

// inodeOf returns the inode number of path.
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("inode identity unavailable on this platform")
	}
	return st.Ino
}

// leaseFiles returns every *.lock file under root.
func leaseFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".lock") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}
