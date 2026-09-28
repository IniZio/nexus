// Command genartifacts fetches every pinned host executable for a target
// GOARCH, verifies sha256, and zstd-compresses into the gitignored go:embed
// dir so that go:embed picks them up at build time.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/hostbin"
	"github.com/klauspost/compress/zstd"
)

func main() {
	goarch := flag.String("goarch", runtime.GOARCH, "target GOARCH (e.g. amd64, arm64)")
	out := flag.String("out", "", "output directory for .zst files (required)")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "genartifacts: -out is required")
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "genartifacts: mkdir %s: %v\n", *out, err)
		os.Exit(1)
	}

	pins := hostbin.Pins()
	names := make([]string, 0, len(pins))
	for name := range pins {
		names = append(names, name)
	}
	sort.Strings(names)

	client := &http.Client{Timeout: 5 * time.Minute}
	ctx := context.Background()

	downloadable := make(map[string]bool, len(pins))
	exitCode := 0

	for _, name := range names {
		p := pins[name]
		if !p.Downloadable(*goarch) {
			fmt.Fprintf(os.Stderr, "skip %s %s (%s): %s\n", name, p.Version, *goarch, p.Note)
			continue
		}
		downloadable[name] = true

		zstPath := filepath.Join(*out, name+".zst")

		// Check cache: decode existing .zst and compare sha256 before hitting the network.
		if cached, err := readAndDecodeZst(zstPath); err == nil {
			want, _ := p.BinarySHA256(*goarch)
			if sha256hex(cached) == want {
				fmt.Printf("cached %s\n", name)
				continue
			}
		}

		data, err := hostbin.FetchVerified(ctx, client, p, *goarch)
		if err != nil {
			if errors.Is(err, hostbin.ErrChecksumMismatch) {
				fmt.Fprintf(os.Stderr, "genartifacts: checksum mismatch for %s — build aborted\n", name)
			} else {
				fmt.Fprintf(os.Stderr, "genartifacts: fetch %s: %v\n", name, err)
			}
			exitCode = 1
			continue
		}

		compressed, err := encodeZst(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "genartifacts: compress %s: %v\n", name, err)
			exitCode = 1
			continue
		}

		// Re-decode and re-verify before committing to disk.
		decoded, err := decodeZst(compressed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "genartifacts: re-decode %s: %v\n", name, err)
			exitCode = 1
			continue
		}
		want, _ := p.BinarySHA256(*goarch)
		if sha256hex(decoded) != want {
			fmt.Fprintf(os.Stderr, "genartifacts: re-verify failed for %s\n", name)
			exitCode = 1
			continue
		}

		if err := atomicWrite(*out, zstPath, compressed); err != nil {
			fmt.Fprintf(os.Stderr, "genartifacts: write %s: %v\n", name, err)
			exitCode = 1
			continue
		}
		fmt.Printf("wrote %s %s raw=%d zst=%d\n", name, p.Version, len(data), len(compressed))
	}

	// Remove stale .zst files whose pin is no longer downloadable for this arch.
	entries, err := os.ReadDir(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genartifacts: readdir %s: %v\n", *out, err)
		os.Exit(1)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zst") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".zst")
		if downloadable[base] {
			continue
		}
		stale := filepath.Join(*out, e.Name())
		if err := os.Remove(stale); err != nil {
			fmt.Fprintf(os.Stderr, "genartifacts: remove stale %s: %v\n", e.Name(), err)
		} else {
			fmt.Printf("removed stale %s\n", e.Name())
		}
	}

	os.Exit(exitCode)
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func encodeZst(data []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, err
	}
	return enc.EncodeAll(data, nil), nil
}

func decodeZst(data []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(data, nil)
}

// readAndDecodeZst reads a .zst file from disk and decompresses it.
func readAndDecodeZst(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeZst(raw)
}

// atomicWrite writes data to dst via a temp file in dir, syncing before rename.
func atomicWrite(dir, dst string, data []byte) (retErr error) {
	tmp, err := os.CreateTemp(dir, ".tmp-genartifacts-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
