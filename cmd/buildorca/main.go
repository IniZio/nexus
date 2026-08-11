// buildorca — one-shot driver: builds the nexus3 agent base image and registers
// it in the nexus3 image cache as "nexus3-orca:latest".
//
// NOT committed. Scratch build tool for demo/ops use.
// Usage: HOME=$HOME TMPDIR=/tmp go run ./cmd/buildorca  (from repo root)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/test/selfhost"
)

func main() {
	// Handle netns re-exec sentinel first — this binary is used as the
	// re-exec image by the CHDriver when setting up network namespaces.
	if os.Getenv(cloudhypervisor.NetnsRunEnv) == "1" {
		cloudhypervisor.RunNetnsChild()
		// RunNetnsChild never returns normally; os.Exit is called inside.
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "-smoke" {
		if err := smokeboot(); err != nil {
			fmt.Fprintf(os.Stderr, "smokeboot: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "buildorca: %v\n", err)
		os.Exit(1)
	}
}

// metaRecord mirrors imageRecord in internal/core/image/cache.go.
type metaRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Digest        domain.Digest    `json:"digest"`
	Ref           string           `json:"ref"`
	Kind          domain.ImageKind `json:"kind"`
	Size          int64            `json:"size"`
	CreatedAt     time.Time        `json:"created_at"`
}

func run() error {
	ctx := context.Background()

	// Resolve cache root (same logic as production CLI).
	root, err := store.DefaultRoot()
	if err != nil {
		return fmt.Errorf("resolve store root: %w", err)
	}
	cacheRoot := filepath.Join(root, "images")
	fmt.Fprintf(os.Stderr, "buildorca: cache root = %s\n", cacheRoot)

	c, err := image.NewCache(cacheRoot)
	if err != nil {
		return fmt.Errorf("open image cache: %w", err)
	}

	fmt.Fprintln(os.Stderr, "buildorca: starting BuildAgentBaseImage (this takes a while) …")
	img, err := selfhost.BuildAgentBaseImage(ctx, c)
	if err != nil {
		return fmt.Errorf("BuildAgentBaseImage: %w", err)
	}
	fmt.Fprintf(os.Stderr, "buildorca: built image ref=%q digest=%s size=%d\n", img.Ref, img.Digest, img.Size)

	// Patch meta.json to change the Ref to nexus3-orca:latest.
	metaPath := filepath.Join(cacheRoot, img.Digest.Algo(), img.Digest.Hex(), "meta.json")

	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("read meta.json at %s: %w", metaPath, err)
	}

	var rec metaRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("decode meta.json: %w", err)
	}

	oldRef := rec.Ref
	rec.Ref = "nexus3-orca:latest"

	patched, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode meta.json: %w", err)
	}

	// Write atomically: temp file in same dir + rename.
	dir := filepath.Dir(metaPath)
	tmp, err := os.CreateTemp(dir, "meta-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp meta: %w", err)
	}
	if _, err := tmp.Write(patched); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write temp meta: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("close temp meta: %w", err)
	}
	if err := os.Rename(tmp.Name(), metaPath); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rename meta: %w", err)
	}

	fmt.Fprintf(os.Stderr, "buildorca: patched ref %q → %q in %s\n", oldRef, rec.Ref, metaPath)

	// Final verification.
	imgs, err := c.List(ctx)
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nbuildorca: cache contents (%d entries):\n", len(imgs))
	found := false
	for _, im := range imgs {
		fmt.Fprintf(os.Stderr, "  ref=%-30s digest=%s size=%d\n", im.Ref, im.Digest, im.Size)
		if im.Ref == "nexus3-orca:latest" {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("nexus3-orca:latest not found in cache after patch")
	}

	artifactPath := filepath.Join(cacheRoot, img.Digest.Algo(), img.Digest.Hex(), "artifact")
	fmt.Printf("nexus3-orca:latest digest=%s path=%s\n", img.Digest, artifactPath)
	return nil
}
