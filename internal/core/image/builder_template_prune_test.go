package image_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

const (
	staleTag   = "0123456789abcdef"
	currentTag = "fedcba9876543210"
)

func newRootedCache(t *testing.T) (*image.Cache, string) {
	t.Helper()
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c, root
}

// writeTemplate creates a tiny file under root and, when old is set, pushes its
// mtime well outside BuilderTemplateInFlightGrace.
func writeTemplate(t *testing.T, root, name string, old bool) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if old {
		past := time.Now().Add(-2 * image.BuilderTemplateInFlightGrace)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
	return path
}

func templateName(digestSafe, tag string) string {
	return "nexus-builder-" + digestSafe + "-agent" + tag + ".ext4"
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}

func TestBuilderAgentTag(t *testing.T) {
	if got := image.BuilderAgentTag(nil); got != "" {
		t.Fatalf("empty input: got %q, want \"\"", got)
	}
	got := image.BuilderAgentTag([]byte("agent"))
	if len(got) != 16 {
		t.Fatalf("tag length: got %d (%q), want 16", len(got), got)
	}
	if got != image.BuilderAgentTag([]byte("agent")) {
		t.Fatalf("tag not deterministic")
	}
	if got == image.BuilderAgentTag([]byte("other")) {
		t.Fatalf("different bytes produced the same tag")
	}
}

func TestPruneBuilderTemplatesRemovesStaleKeepsCurrentIgnoresOthers(t *testing.T) {
	c, root := newRootedCache(t)
	stale := writeTemplate(t, root, templateName("aaa", staleTag), true)
	current := writeTemplate(t, root, templateName("aaa", currentTag), true)
	foo := writeTemplate(t, root, "foo.ext4", true)
	noTag := writeTemplate(t, root, "nexus-builder-x.ext4", true)
	shortTag := writeTemplate(t, root, "nexus-builder-x-agentabc.ext4", true)
	nonHex := writeTemplate(t, root, templateName("x", "zzzzzzzzzzzzzzzz"), true)

	listed, err := c.ListBuilderTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListBuilderTemplates: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d templates, want 2: %+v", len(listed), listed)
	}
	for _, tpl := range listed {
		if tpl.Size <= 0 {
			t.Fatalf("template %s has non-positive allocated size %d", tpl.Path, tpl.Size)
		}
	}

	removed, freed, err := c.PruneBuilderTemplates(context.Background(), currentTag, false)
	if err != nil {
		t.Fatalf("PruneBuilderTemplates: %v", err)
	}
	if len(removed) != 1 || removed[0].Path != stale || removed[0].AgentTag != staleTag {
		t.Fatalf("removed = %+v, want only %s", removed, stale)
	}
	if freed <= 0 {
		t.Fatalf("freed = %d, want > 0", freed)
	}
	if exists(t, stale) {
		t.Fatalf("stale template still on disk")
	}
	for _, p := range []string{current, foo, noTag, shortTag, nonHex} {
		if !exists(t, p) {
			t.Fatalf("%s was removed, want kept", p)
		}
	}
}

func TestPruneBuilderTemplatesKeepsHeldFile(t *testing.T) {
	c, root := newRootedCache(t)
	held := writeTemplate(t, root, templateName("held", staleTag), true)
	free := writeTemplate(t, root, templateName("free", staleTag), true)

	holder, err := os.Open(held)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("flock holder: %v", err)
	}

	// flock attaches to the open file description: a second open in the same
	// process must conflict, otherwise the KEEP assertion below is vacuous.
	probe, err := os.Open(held)
	if err != nil {
		t.Fatalf("open probe: %v", err)
	}
	probeErr := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	probe.Close()
	if probeErr != syscall.EWOULDBLOCK && probeErr != syscall.EAGAIN {
		t.Fatalf("second-descriptor flock probe = %v, want EWOULDBLOCK", probeErr)
	}

	removed, _, err := c.PruneBuilderTemplates(context.Background(), currentTag, false)
	if err != nil {
		t.Fatalf("PruneBuilderTemplates: %v", err)
	}
	if len(removed) != 1 || removed[0].Path != free {
		t.Fatalf("removed = %+v, want only %s", removed, free)
	}
	if !exists(t, held) {
		t.Fatalf("held template was removed")
	}
	if exists(t, free) {
		t.Fatalf("free template still on disk")
	}
}

func TestPruneBuilderTemplatesKeepsFreshFile(t *testing.T) {
	c, root := newRootedCache(t)
	fresh := writeTemplate(t, root, templateName("fresh", staleTag), false)

	removed, freed, err := c.PruneBuilderTemplates(context.Background(), currentTag, false)
	if err != nil {
		t.Fatalf("PruneBuilderTemplates: %v", err)
	}
	if len(removed) != 0 || freed != 0 {
		t.Fatalf("removed = %+v freed = %d, want nothing", removed, freed)
	}
	if !exists(t, fresh) {
		t.Fatalf("fresh template was removed")
	}
}

func TestPruneBuilderTemplatesDryRun(t *testing.T) {
	c, root := newRootedCache(t)
	stale := writeTemplate(t, root, templateName("dry", staleTag), true)

	removed, freed, err := c.PruneBuilderTemplates(context.Background(), currentTag, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(removed) != 1 || removed[0].Path != stale {
		t.Fatalf("dry run removed = %+v, want %s", removed, stale)
	}
	if freed <= 0 {
		t.Fatalf("dry run freed = %d, want > 0", freed)
	}
	if !exists(t, stale) {
		t.Fatalf("dry run unlinked the file")
	}

	removed, freed, err = c.PruneBuilderTemplates(context.Background(), currentTag, false)
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if len(removed) != 1 || removed[0].Path != stale || freed <= 0 {
		t.Fatalf("real run removed = %+v freed = %d", removed, freed)
	}
	if exists(t, stale) {
		t.Fatalf("real run left the file on disk")
	}
}

func TestPruneBuilderTemplatesEmptyTagRemovesNothing(t *testing.T) {
	c, root := newRootedCache(t)
	stale := writeTemplate(t, root, templateName("empty", staleTag), true)

	removed, freed, err := c.PruneBuilderTemplates(context.Background(), "", false)
	if err != nil {
		t.Fatalf("PruneBuilderTemplates: %v", err)
	}
	if removed != nil || freed != 0 {
		t.Fatalf("removed = %+v freed = %d, want nil, 0", removed, freed)
	}
	if !exists(t, stale) {
		t.Fatalf("template removed with empty currentTag")
	}
}

func TestListBuilderTemplatesMissingRoot(t *testing.T) {
	c, root := newRootedCache(t)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	got, err := c.ListBuilderTemplates(context.Background())
	if err != nil || got != nil {
		t.Fatalf("missing root: got %+v, %v; want nil, nil", got, err)
	}
}

func TestPruneCandidates(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	var digests []domain.Digest
	for _, content := range []string{"one", "two", "three"} {
		img, r := makeImage([]byte(content))
		img.Ref = "nexus3-test:" + content
		if err := c.Put(ctx, img, r); err != nil {
			t.Fatalf("Put %s: %v", content, err)
		}
		digests = append(digests, img.Digest)
	}

	got, err := c.PruneCandidates(ctx, []domain.Digest{digests[0]})
	if err != nil {
		t.Fatalf("PruneCandidates: %v", err)
	}
	want := map[domain.Digest]bool{digests[1]: true, digests[2]: true}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	for _, img := range got {
		if !want[img.Digest] {
			t.Fatalf("unexpected candidate %s", img.Digest)
		}
	}

	for _, d := range digests {
		if _, err := c.Get(ctx, d); err != nil {
			t.Fatalf("entry %s disturbed by PruneCandidates: %v", d, err)
		}
	}
}
