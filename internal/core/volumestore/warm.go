package volumestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/store"
)

const WarmDirName = ".warm"

type WarmKind string

const (
	WarmKindDocker  WarmKind = "docker"
	WarmKindGoCache WarmKind = "gocache"
	WarmKindGoPath  WarmKind = "gopath"
)

var ErrReflinkUnsupported = errors.New("volumestore: reflink (FICLONE) not supported on this filesystem")

var (
	ErrWarmNotReplaced = errors.New("volumestore: warm copy not replaced (candidate too small and existing too fresh)")
	ErrWarmTooLarge    = errors.New("volumestore: warm copy too large")
)

var (
	WarmShrinkRatio   = 0.5
	WarmStaleAfter    = 7 * 24 * time.Hour
	WarmMaxCopyBytes  = int64(16 << 30) // 16 GiB
	WarmMaxTotalBytes = int64(64 << 30) // 64 GiB
)

type WarmMeta struct {
	SourceVolume   string    `json:"source_volume"`
	PromotedAt     time.Time `json:"promoted_at"`
	SizeBytes      int64     `json:"size_bytes"`
	AllocatedBytes int64     `json:"allocated_bytes"`
}

type WarmEntry struct {
	ProjectKey string
	Kind       WarmKind
	Meta       WarmMeta
}

var validWarmKinds = map[WarmKind]struct{}{
	WarmKindDocker:  {},
	WarmKindGoCache: {},
	WarmKindGoPath:  {},
}

var warmSlugRE = regexp.MustCompile(`[^a-z0-9._-]+`)

// ProjectKey derives a stable, unique identifier from an absolute git-common-dir path.
func ProjectKey(gitCommonDir string) string {
	cleaned := filepath.Clean(gitCommonDir)
	base := filepath.Base(cleaned)
	var repoBase string
	if base == ".git" {
		repoBase = filepath.Base(filepath.Dir(cleaned))
	} else {
		repoBase = strings.TrimSuffix(base, ".git")
	}
	slug := warmSlugRE.ReplaceAllString(strings.ToLower(repoBase), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "repo"
	}
	h := sha256.Sum256([]byte(cleaned))
	return slug + "-" + hex.EncodeToString(h[:])[:12]
}

func (s *VolumeStore) warmKindDir(projectKey string, kind WarmKind) string {
	return filepath.Join(s.root, WarmDirName, projectKey, string(kind))
}

func (s *VolumeStore) WarmDiskPath(projectKey string, kind WarmKind) string {
	return filepath.Join(s.warmKindDir(projectKey, kind), diskFile)
}

func (s *VolumeStore) warmMetaPath(projectKey string, kind WarmKind) string {
	return filepath.Join(s.warmKindDir(projectKey, kind), metaFile)
}

func validateWarmKind(kind WarmKind) error {
	if _, ok := validWarmKinds[kind]; !ok {
		return fmt.Errorf("volumestore: unknown warm kind %q", kind)
	}
	return nil
}

func validateProjectKey(key string) error {
	if key == "" || strings.Contains(key, "/") || strings.Contains(key, "..") {
		return fmt.Errorf("volumestore: invalid project key %q", key)
	}
	return nil
}

// reflinkFileFn and sparseCopyFileFn are injectable for tests.
var reflinkFileFn = reflinkFile
var sparseCopyFileFn = sparseCopyFile

// copyDiskFile copies src into dst, preferring reflink (FICLONE) and falling
// back to sparse copy when reflink is unsupported. Returns the method used.
// ctx is checked between copy chunks by the sparse-copy path.
func copyDiskFile(ctx context.Context, dst, src *os.File) (method string, err error) {
	if err := reflinkFileFn(dst, src); err == nil {
		return "reflink", nil
	} else if !errors.Is(err, ErrReflinkUnsupported) {
		return "", err
	}
	if err := sparseCopyFileFn(ctx, dst, src); err != nil {
		return "", err
	}
	return "sparse-copy", nil
}

// cleanupStaleTmpFiles removes disk.ext4.tmp-* and meta.json.tmp-* files in
// dir that are older than 1 hour (left by a killed PromoteToWarm).
func cleanupStaleTmpFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, "disk.ext4.tmp-") && !strings.HasPrefix(n, "meta.json.tmp-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// SeedFromWarm seeds a new volume from the warm store for (projectKey, kind).
// Returns (true, nil) on success; (false, nil) if the volume already exists or no warm copy.
// Returns (false, err) on failure — callers should fall back to normal Create.
func (s *VolumeStore) SeedFromWarm(ctx context.Context, name, projectKey string, kind WarmKind, sizeBytes int64) (bool, error) {
	if err := validateWarmKind(kind); err != nil {
		return false, err
	}
	if err := validateProjectKey(projectKey); err != nil {
		return false, err
	}
	if _, err := s.readRecord(name); err == nil {
		return false, nil
	}
	warmDisk := s.WarmDiskPath(projectKey, kind)
	if _, err := os.Stat(warmDisk); err != nil {
		return false, nil
	}
	if sizeBytes <= 0 {
		sizeBytes = DefaultDiskSizeBytes
	}
	dir := s.volDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("volumestore: seed mkdir %s: %w", name, err)
	}
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		return false, fmt.Errorf("volumestore: seed open lock %s: %w", name, err)
	}
	defer lk.Close() //nolint:errcheck // advisory lock fd close errors are non-actionable in a defer
	lockCtx, lockCancel := context.WithTimeout(ctx, createLockTimeout)
	defer lockCancel()
	if err := lk.TryExclusive(lockCtx); err != nil {
		return false, fmt.Errorf("volumestore: seed acquire lock %s: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck // advisory lock release errors are non-fatal; fd close covers it
	if _, err := s.readRecord(name); err == nil {
		return false, nil
	}
	dstDisk := s.DiskPath(name)
	srcF, err := os.Open(warmDisk)
	if err != nil {
		return false, fmt.Errorf("volumestore: seed open warm disk: %w", err)
	}
	defer srcF.Close() //nolint:errcheck // read-only fd; close error is non-actionable
	allocBytes, statErr := allocatedFileBytes(srcF)
	if statErr != nil {
		return false, fmt.Errorf("volumestore: seed stat warm disk: %w", statErr)
	}
	if err := s.checkFreeSpace(dir, allocBytes); err != nil {
		return false, err
	}
	dstF, err := os.OpenFile(dstDisk, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false, fmt.Errorf("volumestore: seed create dst disk: %w", err)
	}
	t0 := time.Now()
	method, copyErr := copyDiskFile(ctx, dstF, srcF)
	if copyErr != nil {
		dstF.Close()
		_ = os.Remove(dstDisk)
		return false, fmt.Errorf("volumestore: seed copy: %w", copyErr)
	}
	if err := dstF.Sync(); err != nil {
		dstF.Close()
		_ = os.Remove(dstDisk)
		return false, fmt.Errorf("volumestore: seed fsync: %w", err)
	}
	fi, err := dstF.Stat()
	dstF.Close()
	if err != nil {
		_ = os.Remove(dstDisk)
		return false, fmt.Errorf("volumestore: seed stat clone: %w", err)
	}
	slog.Info("volumestore: seeded from warm", "volume", name, "project_key", projectKey, "kind", kind, "method", method, "elapsed", time.Since(t0))
	if fi.Size() < sizeBytes {
		if err := os.Truncate(dstDisk, sizeBytes); err != nil {
			_ = os.Remove(dstDisk)
			return false, fmt.Errorf("volumestore: seed truncate: %w", err)
		}
		growCtx, growCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer growCancel()
		if err := runE2fsckClean(growCtx, dstDisk); err != nil {
			_ = os.Remove(dstDisk)
			return false, fmt.Errorf("volumestore: seed e2fsck: %w", err)
		}
		if err := runResize2fs(growCtx, dstDisk); err != nil {
			_ = os.Remove(dstDisk)
			return false, fmt.Errorf("volumestore: seed resize2fs: %w", err)
		}
	}
	finalStat, err := os.Stat(dstDisk)
	if err != nil {
		_ = os.Remove(dstDisk)
		return false, fmt.Errorf("volumestore: seed stat final: %w", err)
	}
	rec := &VolumeRecord{
		Name:      name,
		Kind:      KindDisk,
		SizeBytes: finalStat.Size(),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.writeRecord(rec); err != nil {
		_ = os.Remove(dstDisk)
		return false, fmt.Errorf("volumestore: seed write record: %w", err)
	}
	return true, nil
}

// PromoteToWarm copies a volume's disk into the warm store under (projectKey, kind).
// The volume must be kind=disk with no current attachments.
func (s *VolumeStore) PromoteToWarm(ctx context.Context, name, projectKey string, kind WarmKind) error {
	if err := validateWarmKind(kind); err != nil {
		return err
	}
	if err := validateProjectKey(projectKey); err != nil {
		return err
	}
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		return fmt.Errorf("volumestore: promote open lock %s: %w", name, err)
	}
	defer lk.Close() //nolint:errcheck // advisory lock fd close errors are non-actionable in a defer
	promoteCtx, promoteCancel := context.WithTimeout(ctx, 30*time.Second)
	defer promoteCancel()
	if err := lk.TryExclusive(promoteCtx); err != nil {
		return fmt.Errorf("volumestore: promote acquire lock %s: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck // advisory lock release errors are non-fatal; fd close covers it
	rec, err := s.readRecord(name)
	if err != nil {
		return fmt.Errorf("volumestore: promote read record: %w", err)
	}
	if rec.Kind != KindDisk {
		return fmt.Errorf("volumestore: promote: volume %s is not kind=disk (got %s)", name, rec.Kind)
	}
	if len(rec.Attachments) > 0 {
		return fmt.Errorf("volumestore: promote: volume %s is still attached", name)
	}
	wDir := s.warmKindDir(projectKey, kind)
	if err := os.MkdirAll(wDir, 0o755); err != nil {
		return fmt.Errorf("volumestore: promote mkdir warm: %w", err)
	}
	cleanupStaleTmpFiles(wDir)
	pid := os.Getpid()
	tmpDisk := filepath.Join(wDir, fmt.Sprintf("disk.ext4.tmp-%d-%d", pid, rand.Int63()))
	tmpMeta := filepath.Join(wDir, fmt.Sprintf("meta.json.tmp-%d-%d", pid, rand.Int63()))
	srcF, err := os.Open(s.DiskPath(name))
	if err != nil {
		return fmt.Errorf("volumestore: promote open src disk: %w", err)
	}
	defer srcF.Close() //nolint:errcheck // read-only fd; close error is non-actionable
	allocBytes, statErr := allocatedFileBytes(srcF)
	if statErr != nil {
		return fmt.Errorf("volumestore: promote stat src disk: %w", statErr)
	}
	if allocBytes > WarmMaxCopyBytes {
		return fmt.Errorf("%w: candidate %d bytes > limit %d", ErrWarmTooLarge, allocBytes, WarmMaxCopyBytes)
	}
	if existAlloc, existAt, ok := s.readWarmMetaForGuard(projectKey, kind); ok && existAlloc > 0 {
		threshold := int64(float64(existAlloc) * WarmShrinkRatio)
		if allocBytes < threshold && time.Since(existAt) < WarmStaleAfter {
			return fmt.Errorf("%w: candidate %d bytes < %.0f%% of existing %d bytes", ErrWarmNotReplaced, allocBytes, WarmShrinkRatio*100, existAlloc)
		}
	}
	if err := s.checkFreeSpace(wDir, allocBytes); err != nil {
		return err
	}
	dstF, err := os.OpenFile(tmpDisk, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("volumestore: promote create tmp disk: %w", err)
	}
	t0 := time.Now()
	method, copyErr := copyDiskFile(ctx, dstF, srcF)
	if copyErr != nil {
		dstF.Close()
		_ = os.Remove(tmpDisk)
		return fmt.Errorf("volumestore: promote copy: %w", copyErr)
	}
	if err := dstF.Sync(); err != nil {
		dstF.Close()
		_ = os.Remove(tmpDisk)
		return fmt.Errorf("volumestore: promote fsync tmp disk: %w", err)
	}
	dstF.Close()
	slog.Info("volumestore: promoted to warm", "volume", name, "project_key", projectKey, "kind", kind, "method", method, "elapsed", time.Since(t0))
	meta := WarmMeta{
		SourceVolume:   name,
		PromotedAt:     time.Now().UTC(),
		SizeBytes:      rec.SizeBytes,
		AllocatedBytes: allocBytes,
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		_ = os.Remove(tmpDisk)
		return fmt.Errorf("volumestore: promote marshal meta: %w", err)
	}
	if err := os.WriteFile(tmpMeta, metaData, 0o644); err != nil {
		_ = os.Remove(tmpDisk)
		return fmt.Errorf("volumestore: promote write tmp meta: %w", err)
	}
	if mf, err := os.OpenFile(tmpMeta, os.O_RDWR, 0); err == nil {
		_ = mf.Sync()
		mf.Close()
	}
	finalDisk := s.WarmDiskPath(projectKey, kind)
	finalMeta := s.warmMetaPath(projectKey, kind)
	if err := os.Rename(tmpDisk, finalDisk); err != nil {
		_ = os.Remove(tmpDisk)
		_ = os.Remove(tmpMeta)
		return fmt.Errorf("volumestore: promote rename disk: %w", err)
	}
	if err := os.Rename(tmpMeta, finalMeta); err != nil {
		_ = os.Remove(tmpMeta)
		return fmt.Errorf("volumestore: promote rename meta: %w", err)
	}
	if dirF, err := os.Open(wDir); err == nil {
		_ = dirF.Sync()
		dirF.Close()
	}
	s.evictOverTotalCap(projectKey, kind)
	return nil
}

// ListWarm enumerates all warm entries under .warm/<project>/<kind>/.
func (s *VolumeStore) ListWarm() ([]WarmEntry, error) {
	warmRoot := filepath.Join(s.root, WarmDirName)
	projEntries, err := os.ReadDir(warmRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("volumestore: list warm: %w", err)
	}
	var out []WarmEntry
	for _, pe := range projEntries {
		if !pe.IsDir() {
			continue
		}
		projKey := pe.Name()
		kindEntries, err := os.ReadDir(filepath.Join(warmRoot, projKey))
		if err != nil {
			continue
		}
		for _, ke := range kindEntries {
			if !ke.IsDir() {
				continue
			}
			kind := WarmKind(ke.Name())
			metaPath := filepath.Join(warmRoot, projKey, string(kind), metaFile)
			data, err := os.ReadFile(metaPath)
			if err != nil {
				continue
			}
			var m WarmMeta
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			out = append(out, WarmEntry{ProjectKey: projKey, Kind: kind, Meta: m})
		}
	}
	return out, nil
}

// readWarmMetaForGuard returns the existing copy's allocated bytes, PromotedAt, and presence.
func (s *VolumeStore) readWarmMetaForGuard(projectKey string, kind WarmKind) (allocBytes int64, promotedAt time.Time, ok bool) {
	data, err := os.ReadFile(s.warmMetaPath(projectKey, kind))
	if err != nil {
		return 0, time.Time{}, false
	}
	var m WarmMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return 0, time.Time{}, false
	}
	ab := m.AllocatedBytes
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	if _, hasField := raw["allocated_bytes"]; !hasField {
		f, err := os.Open(s.WarmDiskPath(projectKey, kind))
		if err == nil {
			ab, _ = allocatedFileBytes(f)
			f.Close()
		}
	}
	return ab, m.PromotedAt, true
}

// warmEntryAllocBytes returns an entry's allocated bytes, stat-ing disk.ext4 when meta lacks them.
func (s *VolumeStore) warmEntryAllocBytes(e WarmEntry) int64 {
	if e.Meta.AllocatedBytes > 0 {
		return e.Meta.AllocatedBytes
	}
	f, err := os.Open(s.WarmDiskPath(e.ProjectKey, e.Kind))
	if err != nil {
		return 0
	}
	defer f.Close()
	b, _ := allocatedFileBytes(f)
	return b
}

// evictOverTotalCap evicts oldest copies until under WarmMaxTotalBytes, sparing the skip entry.
func (s *VolumeStore) evictOverTotalCap(skipProjectKey string, skipKind WarmKind) {
	warmRoot := filepath.Join(s.root, WarmDirName)
	entries, err := s.ListWarm()
	if err != nil {
		slog.Info("volumestore: eviction list error", "err", err)
		return
	}
	var total int64
	for _, e := range entries {
		total += s.warmEntryAllocBytes(e)
	}
	if total <= WarmMaxTotalBytes {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Meta.PromotedAt.Before(entries[j].Meta.PromotedAt)
	})
	for _, e := range entries {
		if total <= WarmMaxTotalBytes {
			break
		}
		if e.ProjectKey == skipProjectKey && e.Kind == skipKind {
			continue
		}
		allocB := s.warmEntryAllocBytes(e)
		kindDir := s.warmKindDir(e.ProjectKey, e.Kind)
		if err := os.RemoveAll(kindDir); err != nil {
			slog.Info("volumestore: eviction remove error", "dir", kindDir, "err", err)
			continue
		}
		total -= allocB
		slog.Info("volumestore: evicted warm copy", "project_key", e.ProjectKey, "kind", e.Kind, "allocated_bytes", allocB)
		projDir := filepath.Join(warmRoot, e.ProjectKey)
		if remaining, err := os.ReadDir(projDir); err == nil && len(remaining) == 0 {
			_ = os.Remove(projDir)
		}
	}
}

// RemoveWarm removes warm entries for projectKey (or all if projectKey is "").
func (s *VolumeStore) RemoveWarm(projectKey string) ([]WarmEntry, error) {
	all, err := s.ListWarm()
	if err != nil {
		return nil, err
	}
	warmRoot := filepath.Join(s.root, WarmDirName)
	if projectKey == "" {
		if err := os.RemoveAll(warmRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("volumestore: remove all warm: %w", err)
		}
		return all, nil
	}
	var removed []WarmEntry
	for _, e := range all {
		if e.ProjectKey == projectKey {
			removed = append(removed, e)
		}
	}
	projDir := filepath.Join(warmRoot, projectKey)
	if err := os.RemoveAll(projDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("volumestore: remove warm %s: %w", projectKey, err)
	}
	return removed, nil
}
