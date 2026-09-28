package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/hostbin/pin"
)

// fakeKernelPins returns a kernelPins func that serves the given sha as the
// amd64 sha, downloadable from NEXUS_RELEASE_BASE_URL.
func fakeKernelPins(sha string) func() map[string]pin.Pin {
	return func() map[string]pin.Pin {
		return map[string]pin.Pin{
			kernelPinName: {
				Name:    kernelPinName,
				Version: "1.0.0",
				SHA256ByGoArch: map[string]string{
					"amd64": sha,
					"arm64": sha,
				},
			},
		}
	}
}

func TestKernelInstall(t *testing.T) {
	kernelBytes := []byte("fake-kernel-content")
	h := sha256.Sum256(kernelBytes)
	correctHash := hex.EncodeToString(h[:])

	wrongHash := strings.Repeat("a", 64)

	tests := []struct {
		name         string
		pinSHA       string
		kernelBody   []byte
		kernelStatus int
		preInstall   bool
		wantErr      bool
		wantErrMsg   string
		wantOutput   string
		noKernelReq  bool
	}{
		{
			name:         "fresh install",
			pinSHA:       correctHash,
			kernelBody:   kernelBytes,
			kernelStatus: http.StatusOK,
			wantOutput:   "kernel installed:",
		},
		{
			name:        "already installed",
			pinSHA:      correctHash,
			preInstall:  true,
			noKernelReq: true,
			wantOutput:  "already installed",
		},
		{
			name:         "bad checksum",
			pinSHA:       wrongHash,
			kernelBody:   kernelBytes,
			kernelStatus: http.StatusOK,
			wantErr:      true,
			wantErrMsg:   "sha256 mismatch",
		},
		{
			name:         "404",
			pinSHA:       correctHash,
			kernelStatus: http.StatusNotFound,
			wantErr:      true,
			wantErrMsg:   "not found",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			xdgDir := t.TempDir()
			t.Setenv("XDG_DATA_HOME", xdgDir)

			kernelDest := filepath.Join(xdgDir, "nexus", "images", "kernel", "vmlinux-x86_64")

			if tc.preInstall {
				if err := os.MkdirAll(filepath.Dir(kernelDest), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(kernelDest, kernelBytes, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			orig := kernelPins
			kernelPins = fakeKernelPins(tc.pinSHA)
			t.Cleanup(func() { kernelPins = orig })

			kernelRequested := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				kernelRequested = true
				w.WriteHeader(tc.kernelStatus)
				if tc.kernelStatus == http.StatusOK {
					w.Write(tc.kernelBody) //nolint:errcheck
				}
			}))
			defer srv.Close()

			t.Setenv("NEXUS_RELEASE_BASE_URL", srv.URL)

			var stdout, stderr bytes.Buffer
			out := NewOutput(&stdout, &stderr, false)

			cmd, ok := Lookup("kernel install")
			if !ok {
				t.Fatal("kernel install not registered")
			}

			err := cmd.Run(t.Context(), nil, out)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrMsg)
				}
				if !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrMsg)
				}
				if !tc.preInstall {
					if _, statErr := os.Stat(kernelDest); statErr == nil {
						t.Error("destination file should be absent after error")
					}
				}
				tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(kernelDest), "vmlinux-*.tmp"))
				if len(tmps) > 0 {
					t.Errorf("temp file(s) not cleaned up: %v", tmps)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantOutput != "" && !strings.Contains(stdout.String(), tc.wantOutput) {
				t.Errorf("output %q does not contain %q", stdout.String(), tc.wantOutput)
			}

			if tc.noKernelReq && kernelRequested {
				t.Error("kernel binary was fetched but should not have been (idempotency)")
			}

			if !tc.preInstall {
				if _, err := os.Stat(kernelDest); err != nil {
					t.Errorf("kernel not installed at %s: %v", kernelDest, err)
				}
				got, err := fileSHA256(kernelDest)
				if err != nil {
					t.Fatalf("fileSHA256: %v", err)
				}
				if got != correctHash {
					t.Errorf("installed file hash %s != expected %s", got, correctHash)
				}
			}
		})
	}
}

func TestKernelInstall_DevBuildUsesPin(t *testing.T) {
	kernelBytes := []byte("dev-kernel")
	h := sha256.Sum256(kernelBytes)
	hash := hex.EncodeToString(h[:])

	orig := kernelPins
	kernelPins = fakeKernelPins(hash)
	t.Cleanup(func() { kernelPins = orig })

	xdgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdgDir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s", kernelBytes)
	}))
	defer srv.Close()
	t.Setenv("NEXUS_RELEASE_BASE_URL", srv.URL)

	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)
	cmd, _ := Lookup("kernel install")
	if err := cmd.Run(t.Context(), nil, out); err != nil {
		t.Fatalf("dev build install failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "kernel installed:") {
		t.Errorf("unexpected output: %q", stdout.String())
	}
}
