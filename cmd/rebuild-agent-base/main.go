// Command rebuild-agent-base rebuilds the nexus3-agent-base ext4 image and
// registers it in the production image cache.
//
// This tool is the canonical way to update the base image after changes to
// cmd/nexus3-agent/** or internal/core/agent/**.  See images/AGENT-REBUILD.md
// for the full rebuild rule and staleness detection procedure.
//
// Usage:
//
//	go run ./cmd/rebuild-agent-base
//
// Prerequisites: docker and mke2fs must be in PATH.
// On a warm Docker layer cache the build takes ~2 minutes.
// On a cold cache (first run) expect 15–30 minutes.
//
// After the build completes, remove stale nexus3-agent-base entries from the
// production image cache so the new image is resolved first:
//
//	go run ./cmd/nexus3 image ls
//
// Entries with ref "nexus3-agent-base" and created_at before today are stale.
// Remove them by deleting their sha256/ subdirectories from the cache:
//
//	~/.local/state/nexus3/images/sha256/<hex>/
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/store"
	selfhost "github.com/IniZio/nexus3/internal/test/selfhost"
)

func main() {
	root, err := store.DefaultRoot()
	if err != nil {
		log.Fatalf("rebuild-agent-base: resolve state directory: %v", err)
	}
	cacheRoot := filepath.Join(root, "images")
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		log.Fatalf("rebuild-agent-base: open image cache: %v", err)
	}

	ctx := context.Background()
	fmt.Fprintln(os.Stdout, "rebuild-agent-base: building nexus3-agent-base image...")
	fmt.Fprintln(os.Stdout, "rebuild-agent-base: (requires docker + mke2fs; ~15-30 min cold, ~2 min warm)")

	img, err := selfhost.BuildAgentBaseImage(ctx, cache)
	if err != nil {
		log.Fatalf("rebuild-agent-base: BuildAgentBaseImage: %v", err)
	}

	fmt.Fprintf(os.Stdout, "rebuild-agent-base: done\n")
	fmt.Fprintf(os.Stdout, "  ref:       %s\n", img.Ref)
	fmt.Fprintf(os.Stdout, "  digest:    %s\n", img.Digest)
	fmt.Fprintf(os.Stdout, "  size:      %d bytes\n", img.Size)
	fmt.Fprintf(os.Stdout, "  created:   %s\n", img.CreatedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Remove stale nexus3-agent-base entries from the cache so this image")
	fmt.Fprintln(os.Stdout, "is resolved first by 'sandbox create --image nexus3-agent-base':")
	fmt.Fprintf(os.Stdout, "  rm -rf %s/sha256/<old-hex>/\n", cacheRoot)
	fmt.Fprintln(os.Stdout, "(run 'go run ./cmd/nexus3 image ls' to find the old digests)")
}
