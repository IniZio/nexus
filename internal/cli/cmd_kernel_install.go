package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
)

func init() {
	Register(Command{
		Name:    "kernel install",
		Summary: "Download and install the guest kernel image from a release",
		Run:     runKernelInstall,
	})
}

func runKernelInstall(ctx context.Context, args []string, out *Output) error {
	if err := requireBackend("kernel install"); err != nil {
		return err
	}
	p, ok := activeKernelPin()
	if !ok {
		return fmt.Errorf("kernel install: no pin found for %q", kernelPinName)
	}

	fs := flag.NewFlagSet("kernel install", flag.ContinueOnError)
	flagVersion := fs.String("version", p.Version, "release version to install (defaults to pinned version)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}

	goarch := runtime.GOARCH
	wantSHA, err := p.SourceSHA256(goarch)
	if err != nil {
		return fmt.Errorf("kernel install: no pinned asset for arch %s", goarch)
	}

	dest := xdgKernelPath()
	if dest == "" {
		return fmt.Errorf("kernel install: cannot determine home directory")
	}

	if existingHash, err := fileSHA256(dest); err == nil && existingHash == wantSHA {
		fmt.Fprintln(out.Stdout(), "kernel already installed and up to date")
		return nil
	}

	url := kernelURL(*flagVersion, goarch)
	if url == "" {
		return fmt.Errorf("kernel install: no download URL for arch %s", goarch)
	}

	if err := fetchKernelTo(ctx, nil, url, dest, wantSHA); err != nil {
		return err
	}

	fmt.Fprintf(out.Stdout(), "kernel installed: %s\n", dest)
	return nil
}

// fileSHA256 returns the hex-encoded SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
