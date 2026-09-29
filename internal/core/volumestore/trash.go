package volumestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/store"
)

const (
	// TrashDir is the subdirectory under the store root used for trashed volumes.
	TrashDir = ".trash"
	// TrashGrace is the default retention period before an entry may be expired.
	TrashGrace = 7 * 24 * time.Hour
)

// trashNow is the time source for trash timestamps; replaced in tests.
var trashNow = time.Now

// trashTimeFmt is the UTC timestamp format embedded in entry names.
const trashTimeFmt = "20060102T150405Z"

// TrashEntry describes one entry in the trash directory.
type TrashEntry struct {
	Name      string    // e.g. "foo-agentcfg@20260926T035808Z"
	Original  string    // original volume name before trashing
	TrashedAt time.Time // when the volume was trashed (UTC)
	Path      string    // absolute path to the trash entry directory
}

func (s *VolumeStore) trashRoot() string {
	return filepath.Join(s.root, TrashDir)
}

func (s *VolumeStore) trashEntryPath(entryName string) string {
	return filepath.Join(s.trashRoot(), entryName)
}

// validateTrashEntry rejects names with path-traversal characters.
func validateTrashEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("trash entry: name must not be empty")
	}
	if strings.Contains(entry, "/") || entry == ".." || strings.HasPrefix(entry, "../") {
		return fmt.Errorf("trash entry %q: invalid name (path traversal)", entry)
	}
	return nil
}

// parseTrashEntry splits "<original>@<ts>[optional-suffix]" into its parts.
// The timestamp occupies the first len(trashTimeFmt) bytes after the last '@'.
func parseTrashEntry(name string) (original string, at time.Time, err error) {
	idx := strings.LastIndex(name, "@")
	if idx < 0 {
		return "", time.Time{}, fmt.Errorf("trash entry %q: missing '@' separator", name)
	}
	original = name[:idx]
	if original == "" {
		return "", time.Time{}, fmt.Errorf("trash entry %q: empty original name", name)
	}
	tail := name[idx+1:]
	// Strip optional disambiguation suffix (e.g. "-1") after the fixed-width ts.
	tsStr := tail
	if len(tsStr) > len(trashTimeFmt) {
		tsStr = tsStr[:len(trashTimeFmt)]
	}
	at, err = time.Parse(trashTimeFmt, tsStr)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("trash entry %q: parse timestamp: %w", name, err)
	}
	return original, at, nil
}

// Trash moves volume name into the trash directory.
//
// Refuses when the volume is attached (mirrors Rm) or does not exist. Returns
// the trash entry name. The move is an os.Rename so it is atomic and lossless
// on a single filesystem.
func (s *VolumeStore) Trash(ctx context.Context, name string) (string, error) {
	// Acquire the same per-volume lock that Rm acquires (D3).
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("volume %s: not found", name)
		}
		return "", fmt.Errorf("volume %s: open lock for trash: %w", name, err)
	}
	defer lk.Close() //nolint:errcheck // advisory lock fd close errors are non-actionable in a defer
	if err := lk.TryExclusive(ctx); err != nil {
		return "", fmt.Errorf("volume %s: acquire lock for trash: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck // advisory lock release errors are non-fatal; fd close covers it

	rec, err := s.readRecord(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("volume %s: not found", name)
		}
		return "", err
	}
	if len(rec.Attachments) > 0 {
		ids := make([]string, len(rec.Attachments))
		for i, a := range rec.Attachments {
			ids[i] = a.SandboxID
		}
		return "", fmt.Errorf("volume %s: volume in use: attached to %s", name, strings.Join(ids, ", "))
	}

	if err := os.MkdirAll(s.trashRoot(), 0o755); err != nil {
		return "", fmt.Errorf("volume %s: mkdir trash: %w", name, err)
	}

	ts := trashNow().UTC()
	entryName := name + "@" + ts.Format(trashTimeFmt)

	// Disambiguate same-second collisions.
	dst := s.trashEntryPath(entryName)
	if _, statErr := os.Stat(dst); statErr == nil {
		for i := 1; ; i++ {
			candidate := entryName + fmt.Sprintf("-%d", i)
			if _, statErr := os.Stat(s.trashEntryPath(candidate)); os.IsNotExist(statErr) {
				entryName = candidate
				dst = s.trashEntryPath(entryName)
				break
			}
		}
	}

	src := s.volDir(name)
	if err := os.Rename(src, dst); err != nil {
		renameErr := fmt.Errorf("volume %s: rename to trash: %w", name, err)
		s.audit(ctx, "volume.trash", []string{name, entryName}, renameErr)
		return "", renameErr
	}
	s.audit(ctx, "volume.trash", []string{name, entryName}, nil)
	return entryName, nil
}

// Restore moves a trash entry back under its original name, or under as if non-empty.
// Refuses if a live volume with the target name already exists or the entry is missing.
func (s *VolumeStore) Restore(ctx context.Context, entry, as string) (string, error) {
	if err := validateTrashEntry(entry); err != nil {
		return "", err
	}
	original, _, err := parseTrashEntry(entry)
	if err != nil {
		return "", err
	}

	destName := original
	if as != "" {
		if err := validateName(as); err != nil {
			return "", err
		}
		destName = as
	}

	// Refuse if a live volume already occupies the target name.
	if _, err := s.readRecord(destName); err == nil {
		return "", fmt.Errorf("volume %s: already exists", destName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("volume %s: check existing: %w", destName, err)
	}

	src := s.trashEntryPath(entry)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return "", fmt.Errorf("trash entry %s: not found", entry)
	}

	dst := s.volDir(destName)
	if err := os.Rename(src, dst); err != nil {
		restoreErr := fmt.Errorf("trash entry %s: restore: %w", entry, err)
		s.audit(ctx, "volume.restore", []string{entry, destName}, restoreErr)
		return "", restoreErr
	}
	s.audit(ctx, "volume.restore", []string{entry, destName}, nil)

	// When restoring under a different name, update meta.json to reflect it.
	if destName != original {
		rec, err := s.readRecord(destName)
		if err != nil {
			return "", fmt.Errorf("volume %s: read after restore: %w", destName, err)
		}
		rec.Name = destName
		if err := s.writeRecord(rec); err != nil {
			return "", fmt.Errorf("volume %s: update meta after restore: %w", destName, err)
		}
	}

	return destName, nil
}

// ListTrash returns all trash entries sorted by TrashedAt. A missing trash
// directory is treated as empty.
func (s *VolumeStore) ListTrash(ctx context.Context) ([]TrashEntry, error) {
	entries, err := os.ReadDir(s.trashRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list trash: %w", err)
	}
	var result []TrashEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		original, ts, err := parseTrashEntry(e.Name())
		if err != nil {
			continue // skip unrecognised entries
		}
		result = append(result, TrashEntry{
			Name:      e.Name(),
			Original:  original,
			TrashedAt: ts,
			Path:      s.trashEntryPath(e.Name()),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TrashedAt.Before(result[j].TrashedAt)
	})
	return result, nil
}

// ExpireTrash permanently deletes trash entries whose TrashedAt is older than
// now-grace. Returns the entry names that were deleted.
func (s *VolumeStore) ExpireTrash(ctx context.Context, grace time.Duration, now time.Time) ([]string, error) {
	entries, err := s.ListTrash(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-grace)
	var deleted []string
	for _, e := range entries {
		if e.TrashedAt.Before(cutoff) {
			if err := os.RemoveAll(e.Path); err != nil && !os.IsNotExist(err) {
				return deleted, fmt.Errorf("expire trash: remove %s: %w", e.Name, err)
			}
			deleted = append(deleted, e.Name)
		}
	}
	if len(deleted) > 0 {
		s.audit(ctx, "volume.trash_expire", deleted, nil)
	}
	return deleted, nil
}
