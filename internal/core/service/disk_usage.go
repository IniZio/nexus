package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// DiskCategory is one row of a DiskUsageReport.
type DiskCategory struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Count         int    `json:"count"`
	Bytes         int64  `json:"bytes"`          // allocated on disk (st_blocks*512)
	ApparentBytes int64  `json:"apparent_bytes"` // st_size; larger than Bytes for sparse files
	Reclaimable   int64  `json:"reclaimable"`    // allocated bytes not referenced (0 where unknown)
	Note          string `json:"note,omitempty"`
}

// DiskUsageReport is the result of DiskUsage.
type DiskUsageReport struct {
	StateDir    string         `json:"state_dir"`
	Categories  []DiskCategory `json:"categories"`
	TotalBytes  int64          `json:"total_bytes"`
	Reclaimable int64          `json:"reclaimable"`
	FreeBytes   uint64         `json:"free_bytes"`
	FloorBytes  uint64         `json:"floor_bytes"` // builder free-space floor
	BelowFloor  bool           `json:"below_floor"`
	Hints       []string       `json:"hints"` // ordered next actions
}

// Category names, in report order.
const (
	DiskCategoryImageCache       = "image cache"
	DiskCategoryBuilderTemplates = "builder templates"
	DiskCategorySandboxDisks     = "sandbox disks"
	DiskCategoryBuildCaches      = "build caches"
	DiskCategoryNamedVolumes     = "named volumes"
	DiskCategorySnapshots        = "snapshots"
	DiskCategorySupervisorLogs   = "supervisor logs"
	DiskCategoryOther            = "other"
)

// DiskUsage walks stateDir and reports what nexus3 owns on disk by category.
// Sizes are allocated bytes (disk images are sparse). Reclaimable is an
// estimate: prune paths still keep lease-held and in-flight items. store nil
// => disks/ and the cache report Reclaimable 0 with a Note; agentTag "" =>
// templates cannot be classified stale. Missing dirs are Count 0; symlinks
// are never followed; unreadable entries are noted, not fatal.
func DiskUsage(ctx context.Context, stateDir string, c *image.Cache, store SandboxImageLister, agentTag string) (DiskUsageReport, error) {
	if err := ctx.Err(); err != nil {
		return DiskUsageReport{}, err
	}
	rep := DiskUsageReport{StateDir: stateDir}

	imagesDir := filepath.Join(stateDir, "images")

	// ── image cache ─────────────────────────────────────────────────────────
	cache := DiskCategory{Name: DiskCategoryImageCache, Path: filepath.Join(imagesDir, "sha256")}
	var cacheBytesSeen int64 // allocated bytes attributed to cache entries, subtracted from images/ leftovers
	if c != nil {
		imgs, err := c.List(ctx)
		if err != nil {
			return DiskUsageReport{}, fmt.Errorf("disk usage: list image cache: %w", err)
		}
		referenced := map[domain.Digest]struct{}{}
		if store != nil {
			ref, err := ReferencedDigests(ctx, c, store)
			if err != nil {
				return DiskUsageReport{}, fmt.Errorf("disk usage: %w", err)
			}
			for _, d := range ref {
				referenced[d] = struct{}{}
			}
		}
		var notes noteSet
		unreferenced := 0
		for _, img := range imgs {
			entry := filepath.Join(cache.Path, img.Digest.Hex())
			s := sizeTree(entry, &notes)
			cache.Count++
			cache.Bytes += s.alloc
			cache.ApparentBytes += s.apparent
			if store != nil {
				if _, ok := referenced[img.Digest]; !ok {
					cache.Reclaimable += s.alloc
					unreferenced++
				}
			}
		}
		cacheBytesSeen = cache.Bytes
		switch {
		case store == nil:
			notes.add("no sandbox store; referenced set unknown")
		case unreferenced > 0:
			notes.add(fmt.Sprintf("%d unreferenced image(s); reclaim with nexus3 image prune", unreferenced))
		}
		cache.Note = notes.String()
	} else {
		cache.Note = "no image cache"
	}
	rep.Categories = append(rep.Categories, cache)

	// ── builder templates ───────────────────────────────────────────────────
	templates := DiskCategory{Name: DiskCategoryBuilderTemplates, Path: imagesDir}
	var templateBytesSeen int64
	if c != nil {
		tpls, err := c.ListBuilderTemplates(ctx)
		if err != nil {
			return DiskUsageReport{}, fmt.Errorf("disk usage: %w", err)
		}
		stale := 0
		for _, t := range tpls {
			templates.Count++
			templates.Bytes += t.Size
			templates.ApparentBytes += apparentSize(t.Path)
			if agentTag != "" && t.AgentTag != agentTag {
				templates.Reclaimable += t.Size
				stale++
			}
		}
		templateBytesSeen = templates.Bytes
		switch {
		case agentTag == "":
			templates.Note = "agent binary not found; cannot tell stale templates from current"
		case stale > 0:
			templates.Note = fmt.Sprintf("%d stale template(s) from previous agent builds; reclaim with nexus3 image prune", stale)
		}
	}
	rep.Categories = append(rep.Categories, templates)

	// ── sandbox disks ───────────────────────────────────────────────────────
	disks := DiskCategory{Name: DiskCategorySandboxDisks, Path: filepath.Join(stateDir, "disks")}
	{
		var notes noteSet
		known := map[string]struct{}{}
		if store != nil {
			sbs, err := store.List(ctx)
			if err != nil {
				return DiskUsageReport{}, fmt.Errorf("disk usage: list sandboxes: %w", err)
			}
			for _, sb := range sbs {
				known[sb.ID.String()] = struct{}{}
			}
		}
		orphans := 0
		for _, e := range readDirOrNote(disks.Path, &notes) {
			name := e.Name()
			s := sizeTree(filepath.Join(disks.Path, name), &notes)
			disks.Count++
			disks.Bytes += s.alloc
			disks.ApparentBytes += s.apparent
			if store == nil {
				continue
			}
			if strings.Contains(name, ".shadow.") || strings.HasSuffix(name, ".intent") {
				continue // reaper-managed, always kept here
			}
			if !strings.HasPrefix(name, "sb-") {
				continue // not sandbox-keyed; cannot attribute
			}
			if !hasKnownIDPrefix(name, known) {
				disks.Reclaimable += s.alloc
				orphans++
			}
		}
		switch {
		case store == nil:
			notes.add("no sandbox store; cannot tell orphaned disks from live")
		case orphans > 0:
			notes.add(fmt.Sprintf("%d disk(s) with no sandbox record; run nexus3 reap", orphans))
		}
		notes.add("shadow disks and .intent markers are reaper-managed; see nexus3 reap")
		disks.Note = notes.String()
	}
	rep.Categories = append(rep.Categories, disks)

	// ── flat directory categories ───────────────────────────────────────────
	rep.Categories = append(rep.Categories,
		dirCategory(DiskCategoryBuildCaches, filepath.Join(stateDir, "caches"), "buildkit cache disks; kept while any build can reuse them"),
		dirCategory(DiskCategoryNamedVolumes, filepath.Join(stateDir, "volumes"), "user data; remove with nexus3 volume rm"),
		dirCategory(DiskCategorySnapshots, filepath.Join(stateDir, "snapshots"), ""),
	)
	sup := dirCategory(DiskCategorySupervisorLogs, filepath.Join(stateDir, "supervisors"), "")
	bsup := dirCategory(DiskCategorySupervisorLogs, filepath.Join(stateDir, "builder-supervisors"), "")
	sup.Count += bsup.Count
	sup.Bytes += bsup.Bytes
	sup.ApparentBytes += bsup.ApparentBytes
	sup.Note = joinNotes(sup.Note, bsup.Note, "includes builder-supervisors")
	rep.Categories = append(rep.Categories, sup)

	// ── other: everything else directly under stateDir ──────────────────────
	other := DiskCategory{Name: DiskCategoryOther, Path: stateDir}
	{
		var notes noteSet
		covered := map[string]bool{
			"disks": true, "caches": true, "volumes": true, "snapshots": true,
			"supervisors": true, "builder-supervisors": true,
		}
		for _, e := range readDirOrNote(stateDir, &notes) {
			name := e.Name()
			if covered[name] {
				continue
			}
			s := sizeTree(filepath.Join(stateDir, name), &notes)
			if name == "images" {
				// images/ minus the cache entries and templates already reported:
				// locks, partial writes, and anything the cache does not list.
				s.alloc -= cacheBytesSeen + templateBytesSeen
				if s.alloc < 0 {
					s.alloc = 0
				}
				s.apparent -= rep.Categories[0].ApparentBytes + rep.Categories[1].ApparentBytes
				if s.apparent < 0 {
					s.apparent = 0
				}
			}
			other.Count++
			other.Bytes += s.alloc
			other.ApparentBytes += s.apparent
		}
		notes.add("store records, sockets, netns, locks, image-cache leftovers")
		other.Note = notes.String()
	}
	rep.Categories = append(rep.Categories, other)

	// ── totals, free space, hints ───────────────────────────────────────────
	for _, cat := range rep.Categories {
		rep.TotalBytes += cat.Bytes
		rep.Reclaimable += cat.Reclaimable
	}
	free, err := freeSpaceFunc(stateDir)
	if err != nil {
		return DiskUsageReport{}, fmt.Errorf("disk usage: %w", err)
	}
	rep.FreeBytes = free
	rep.FloorBytes = uint64(DefaultGCFreeSpaceFloorGiB) << 30
	rep.BelowFloor = free < rep.FloorBytes

	if cache.Reclaimable > 0 || templates.Reclaimable > 0 {
		rep.Hints = append(rep.Hints, "nexus3 image prune")
	}
	if disks.Reclaimable > 0 {
		rep.Hints = append(rep.Hints, "nexus3 reap")
	}
	if rep.BelowFloor && len(rep.Hints) == 0 {
		rep.Hints = append(rep.Hints, "nexus3 reap", "nexus3 sandbox rm <id>", "nexus3 volume rm <name>")
	}
	return rep, nil
}

// hasKnownIDPrefix reports whether name starts with any sandbox ID in known
// followed by end-of-name, '.', or '-'.
func hasKnownIDPrefix(name string, known map[string]struct{}) bool {
	for id := range known {
		if !strings.HasPrefix(name, id) {
			continue
		}
		rest := name[len(id):]
		if rest == "" || rest[0] == '.' || rest[0] == '-' {
			return true
		}
	}
	return false
}

// dirCategory sizes every entry directly under dir as one category; Count is
// the number of top-level entries.
func dirCategory(name, dir, note string) DiskCategory {
	cat := DiskCategory{Name: name, Path: dir}
	var notes noteSet
	for _, e := range readDirOrNote(dir, &notes) {
		s := sizeTree(filepath.Join(dir, e.Name()), &notes)
		cat.Count++
		cat.Bytes += s.alloc
		cat.ApparentBytes += s.apparent
	}
	if note != "" {
		notes.add(note)
	}
	cat.Note = notes.String()
	return cat
}

// readDirOrNote lists dir. A missing dir is empty; any other error is noted.
func readDirOrNote(dir string, notes *noteSet) []fs.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		notes.add("unreadable: " + dir)
	}
	return entries
}

type treeSize struct {
	alloc, apparent int64
}

// sizeTree sums allocated and apparent bytes of path (a file or a directory
// tree) without following symlinks. Unreadable entries are noted and skipped.
func sizeTree(path string, notes *noteSet) treeSize {
	var out treeSize
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				notes.add("unreadable: " + p)
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never follow; a symlink's own bytes are noise
		}
		var st syscall.Stat_t
		if lerr := syscall.Lstat(p, &st); lerr != nil {
			if !errors.Is(lerr, fs.ErrNotExist) {
				notes.add("unreadable: " + p)
			}
			return nil
		}
		out.alloc += st.Blocks * 512
		if d.Type().IsRegular() {
			out.apparent += st.Size
		}
		return nil
	})
	return out
}

func apparentSize(path string) int64 {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return 0
	}
	return st.Size
}

// noteSet collects distinct notes in order; unreadable-path notes are capped.
type noteSet struct {
	seen  map[string]struct{}
	items []string
	unr   int
}

func (n *noteSet) add(s string) {
	if n.seen == nil {
		n.seen = map[string]struct{}{}
	}
	if strings.HasPrefix(s, "unreadable: ") {
		n.unr++
		if n.unr > 3 {
			return
		}
	}
	if _, ok := n.seen[s]; ok {
		return
	}
	n.seen[s] = struct{}{}
	n.items = append(n.items, s)
}

func (n *noteSet) String() string {
	items := n.items
	if n.unr > 3 {
		items = append(append([]string{}, items...), fmt.Sprintf("(%d unreadable entries total)", n.unr))
	}
	return strings.Join(items, "; ")
}

func joinNotes(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}
