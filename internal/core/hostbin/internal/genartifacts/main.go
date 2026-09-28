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
	"os/exec"
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
	agentTag := flag.String("agent-tag", "dev", "build tag embedded in nexus-agent via -X main.agentBuildTag")
	agentPkg := flag.String("agent-pkg", "./cmd/nexus-agent", "Go package path for nexus-agent")
	skipAgent := flag.Bool("skip-agent", false, "skip building nexus-agent (leave existing files untouched)")
	localDir := flag.String("local-dir", "", "directory of locally-built binaries; <dir>/<goarch>/<name> or <dir>/<name> used instead of network fetch")
	virtiofsdDir := flag.String("virtiofsd-dir", os.Getenv("VIRTIOFSD_DIR"), "local dir with pre-built virtiofsd (verified against pin sha256)")
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

	binLocalDirs := map[string]string{
		"mke2fs":    *localDir,
		"e2fsck":    *localDir,
		"resize2fs": *localDir,
		"virtiofsd": *virtiofsdDir,
	}

	downloadable := make(map[string]bool, len(pins))
	exitCode := 0

	for _, name := range names {
		p := pins[name]
		binDir := binLocalDirs[name]
		hasLocal := binDir != ""

		if !hasLocal && !p.Downloadable(*goarch) {
			fmt.Fprintf(os.Stderr, "skip %s %s (%s): %s\n", name, p.Version, *goarch, p.Note)
			continue
		}
		downloadable[name] = true

		zstPath := filepath.Join(*out, name+".zst")

		if cached, err := readAndDecodeZst(zstPath); err == nil {
			want, _ := p.BinarySHA256(*goarch)
			if sha256hex(cached) == want {
				fmt.Printf("cached %s\n", name)
				continue
			}
		}

		var (
			data []byte
			err  error
		)
		useLocal := false
		if binDir != "" {
			data, err = readLocalBinary(binDir, *goarch, name)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(os.Stderr, "genartifacts: local read %s: %v\n", name, err)
				exitCode = 1
				continue
			}
			if err == nil {
				useLocal = true
				want, _ := p.BinarySHA256(*goarch)
				if sha256hex(data) != want {
					fmt.Fprintf(os.Stderr, "genartifacts: checksum mismatch for %s — build aborted\n", name)
					exitCode = 1
					continue
				}
			}
		}
		if !useLocal {
			data, err = hostbin.FetchVerified(ctx, client, p, *goarch)
			if err != nil {
				if errors.Is(err, hostbin.ErrChecksumMismatch) {
					fmt.Fprintf(os.Stderr, "genartifacts: checksum mismatch for %s — build aborted\n", name)
				} else {
					fmt.Fprintf(os.Stderr, "genartifacts: fetch %s: %v\n", name, err)
				}
				exitCode = 1
				continue
			}
		}

		compressed, err := encodeZst(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "genartifacts: compress %s: %v\n", name, err)
			exitCode = 1
			continue
		}

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
		if useLocal {
			localPath := filepath.Join(binDir, *goarch, name)
			if _, statErr := os.Stat(localPath); statErr != nil {
				localPath = filepath.Join(binDir, name)
			}
			fmt.Printf("local %s from %s raw=%d zst=%d\n", name, localPath, len(data), len(compressed))
		} else {
			fmt.Printf("wrote %s %s raw=%d zst=%d\n", name, p.Version, len(data), len(compressed))
		}
	}

	if !*skipAgent {
		if err := buildAgent(*out, *goarch, *agentTag, *agentPkg); err != nil {
			fmt.Fprintf(os.Stderr, "genartifacts: build nexus-agent: %v\n", err)
			exitCode = 1
		}
	}

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
		if downloadable[base] || base == "nexus-agent" {
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

// readLocalBinary returns the contents of <dir>/<goarch>/<name> if it exists
// as a regular file, falling back to <dir>/<name>.
func readLocalBinary(dir, goarch, name string) ([]byte, error) {
	archPath := filepath.Join(dir, goarch, name)
	if fi, err := os.Stat(archPath); err == nil && fi.Mode().IsRegular() {
		return os.ReadFile(archPath)
	}
	flat := filepath.Join(dir, name)
	if fi, err := os.Stat(flat); err == nil && fi.Mode().IsRegular() {
		return os.ReadFile(flat)
	}
	return nil, fmt.Errorf("%w: not found in %s (tried %s/%s and %s)", os.ErrNotExist, dir, goarch, name, name)
}

func buildAgent(out, goarch, tag, pkg string) error {
	tmp, err := os.MkdirTemp("", "genartifacts-agent-")
	if err != nil {
		return fmt.Errorf("mkdirtemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "nexus-agent")
	cmd := exec.Command("go", "build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags", "-s -w -X main.agentBuildTag="+tag,
		"-o", bin,
		pkg,
	)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	data, err := os.ReadFile(bin)
	if err != nil {
		return fmt.Errorf("read binary: %w", err)
	}

	rawSHA := sha256hex(data)

	compressed, err := encodeZst(data)
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}

	decoded, err := decodeZst(compressed)
	if err != nil {
		return fmt.Errorf("re-decode: %w", err)
	}
	if sha256hex(decoded) != rawSHA {
		return fmt.Errorf("re-verify failed")
	}

	zstPath := filepath.Join(out, "nexus-agent.zst")
	if err := atomicWrite(out, zstPath, compressed); err != nil {
		return fmt.Errorf("write zst: %w", err)
	}

	shaPath := filepath.Join(out, "nexus-agent.sha256")
	if err := atomicWrite(out, shaPath, []byte(rawSHA+"\n")); err != nil {
		return fmt.Errorf("write sha256: %w", err)
	}

	fmt.Printf("wrote nexus-agent tag=%s raw=%d zst=%d sha=%s\n", tag, len(data), len(compressed), rawSHA[:12])
	return nil
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

func readAndDecodeZst(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeZst(raw)
}

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
