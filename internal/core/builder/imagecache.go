package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// BuildFingerprint computes a stable hex SHA-256 fingerprint over the four
// inputs that together uniquely identify a builder-VM image output:
//
//  1. containerfileBytes — raw bytes of the Containerfile (or Dockerfile).
//  2. baseImageRef — the FROM image reference extracted from the Containerfile
//     (literal string, not resolved OCI digest; see tradeoff note below).
//  3. agentBytes — raw bytes of the nexus3-agent binary baked into the image.
//  4. contextDir — filesystem path of the build-context directory; hashed as
//     sorted (relpath, size, mtime-unix-ns) tuples after .dockerignore
//     filtering (see tradeoff note below).
//
// # Precision tradeoffs
//
// Base image ref: included as a literal tag string rather than the resolved
// OCI digest (e.g. "ubuntu:22.04", not "ubuntu@sha256:…"). A mutable tag
// that advances on the registry will NOT invalidate the fingerprint — only a
// Containerfile edit does. Accept this for builds that pull images at build
// time (buildkitd detects the layer change on the first build); pin the FROM
// line to a digest if you need stricter invalidation.
//
// Context directory: files are represented by (relpath, size, mtime-unix-ns)
// rather than by full content SHA-256. This is O(n) in file count instead of
// O(total-bytes), which matters on large repos. The staleness tradeoff:
// touching a file without changing its content will invalidate the cache
// (mtime changes), whereas overwriting a file with identical bytes in the same
// clock second on a 1-second-resolution filesystem will NOT. Both sides are
// safe for typical editor + git-checkout workflows.
func BuildFingerprint(
	containerfileBytes []byte,
	baseImageRef string,
	agentBytes []byte,
	contextDir string,
) (string, error) {
	contextHash, err := ContextHashDir(contextDir)
	if err != nil {
		return "", fmt.Errorf("build fingerprint: context hash: %w", err)
	}

	h := sha256.New()
	// Each component is prefixed and NUL-terminated to prevent boundary
	// collisions between components of different lengths.
	writeComp := func(prefix, value string) {
		h.Write([]byte(prefix))
		h.Write([]byte(value))
		h.Write([]byte{0})
	}

	cfHash := sha256.Sum256(containerfileBytes)
	writeComp("containerfile:", hex.EncodeToString(cfHash[:]))
	writeComp("base:", baseImageRef)
	agentHash := sha256.Sum256(agentBytes)
	writeComp("agent:", hex.EncodeToString(agentHash[:]))
	writeComp("context:", contextHash)

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExtractFromRef extracts the image reference from the first FROM instruction
// in a Containerfile. Comments and ARG lines are skipped. --platform flags and
// AS aliases are stripped. Returns "scratch" if no FROM line is found.
func ExtractFromRef(containerfileBytes []byte) string {
	for _, rawLine := range strings.Split(string(containerfileBytes), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "ARG ") {
			continue
		}
		if !strings.HasPrefix(upper, "FROM ") {
			continue
		}
		// FROM [--platform=…] <image> [AS <name>]
		fields := strings.Fields(line)
		for i := 1; i < len(fields); i++ {
			lower := strings.ToLower(fields[i])
			if strings.HasPrefix(lower, "--platform") {
				continue
			}
			if strings.ToUpper(fields[i]) == "AS" {
				break
			}
			return fields[i]
		}
	}
	return "scratch"
}

// ContextHashDir computes a stable hex SHA-256 hash of the build-context
// directory at contextDir. Files excluded by a .dockerignore in that directory
// are omitted (the same ignore rules applied by [ContextToDisk]).
//
// See [BuildFingerprint] for the mtime-based staleness tradeoff.
func ContextHashDir(contextDir string) (string, error) {
	pm, err := loadDockerIgnore(contextDir)
	if err != nil {
		return "", fmt.Errorf("context hash: load .dockerignore: %w", err)
	}

	type entry struct {
		relPath string
		size    int64
		mtimeNs int64
	}
	var entries []entry

	err = filepath.WalkDir(contextDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Apply the same dockerignore filter as ContextToDisk uses.
		if pm != nil {
			excluded, matchErr := pm.MatchesOrParentMatches(rel)
			if matchErr != nil {
				return fmt.Errorf("dockerignore match %q: %w", rel, matchErr)
			}
			if excluded {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, entry{
			relPath: rel,
			size:    info.Size(),
			mtimeNs: info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("context hash: walk %q: %w", contextDir, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].relPath < entries[j].relPath
	})

	h := sha256.New()
	for _, e := range entries {
		// "<relpath>\t<size>\t<mtime_ns>\n" — tab-delimited for readability.
		_, _ = fmt.Fprintf(h, "%s\t%s\t%s\n",
			e.relPath,
			strconv.FormatInt(e.size, 10),
			strconv.FormatInt(e.mtimeNs, 10),
		)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// buildCacheEntryDir returns the directory for a given fingerprint entry.
// Layout: {storeRoot}/build-cache/{fp}/
func buildCacheEntryDir(storeRoot, fp string) string {
	return filepath.Join(storeRoot, "build-cache", fp)
}

// LookupBuildCache checks whether a previously built image exists for the
// given fingerprint. On a cache hit it returns the image.Cache digest string
// (e.g. "sha256:abcd…") and true. On any miss (entry absent, digest file
// unreadable, or the image was pruned from imgCache) it returns ("", false,
// nil). A non-nil error indicates an unexpected I/O failure during the lookup.
//
// The caller is responsible for acquiring a per-fingerprint exclusive lock
// before calling this function when concurrent builds with the same fingerprint
// are possible (see [store.OpenLock]).
func LookupBuildCache(ctx context.Context, storeRoot, fp string, imgCache *image.Cache) (string, bool, error) {
	digestFile := filepath.Join(buildCacheEntryDir(storeRoot, fp), "digest")
	data, err := os.ReadFile(digestFile)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("build cache: read digest %q: %w", digestFile, err)
	}

	digestStr := strings.TrimSpace(string(data))
	d, err := domain.ParseDigest(digestStr)
	if err != nil {
		// Corrupted entry — treat as cache miss so the next build overwrites it.
		slog.Warn("build-cache: corrupted digest file, treating as miss",
			"fp", fp[:min(12, len(fp))], "err", err)
		return "", false, nil
	}

	// Verify the artifact still exists in the image cache; GC may have pruned it.
	if _, err := imgCache.Get(ctx, d); err != nil {
		slog.Info("build-cache: image evicted from cache, treating as miss",
			"fp", fp[:min(12, len(fp))], "digest", digestStr)
		return "", false, nil
	}

	return digestStr, true, nil
}

// StoreBuildCache atomically records the image-cache digest for a fingerprint
// so that future [LookupBuildCache] calls can skip the builder VM entirely.
// Failures are non-fatal: the sandbox boots correctly without the entry; the
// next build will repeat. The caller MUST NOT call this if the build failed —
// only store on success.
func StoreBuildCache(storeRoot, fp, digest string) error {
	dir := buildCacheEntryDir(storeRoot, fp)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("build cache: mkdir %q: %w", dir, err)
	}

	digestFile := filepath.Join(dir, "digest")
	tmp := digestFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(digest+"\n"), 0600); err != nil {
		return fmt.Errorf("build cache: write digest tmp: %w", err)
	}
	if err := os.Rename(tmp, digestFile); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("build cache: rename digest: %w", err)
	}
	slog.Info("build-cache: stored", "fp", fp[:min(12, len(fp))], "digest", digest)
	return nil
}

