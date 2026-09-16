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
)

func TestKernelInstall(t *testing.T) {
	kernelBytes := []byte("fake-kernel-content")
	h := sha256.Sum256(kernelBytes)
	correctHash := hex.EncodeToString(h[:])
	sha256Body := fmt.Sprintf("%s  vmlinux-x86_64\n", correctHash)

	wrongHash := strings.Repeat("a", 64)

	tests := []struct {
		name         string
		sha256Body   string
		sha256Status int
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
			sha256Body:   sha256Body,
			sha256Status: http.StatusOK,
			kernelBody:   kernelBytes,
			kernelStatus: http.StatusOK,
			wantOutput:   "kernel installed:",
		},
		{
			name:         "already installed",
			sha256Body:   sha256Body,
			sha256Status: http.StatusOK,
			preInstall:   true,
			noKernelReq:  true,
			wantOutput:   "already installed",
		},
		{
			name:         "bad checksum",
			sha256Body:   wrongHash + "  vmlinux-x86_64\n",
			sha256Status: http.StatusOK,
			kernelBody:   kernelBytes,
			kernelStatus: http.StatusOK,
			wantErr:      true,
			wantErrMsg:   "sha256 mismatch",
		},
		{
			name:         "404",
			sha256Status: http.StatusNotFound,
			wantErr:      true,
			wantErrMsg:   "not found",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			xdgDir := t.TempDir()
			t.Setenv("XDG_DATA_HOME", xdgDir)

			kernelDest := filepath.Join(xdgDir, "nexus3", "images", "kernel", "vmlinux-x86_64")

			if tc.preInstall {
				if err := os.MkdirAll(filepath.Dir(kernelDest), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(kernelDest, kernelBytes, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			kernelRequested := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, ".sha256") {
					w.WriteHeader(tc.sha256Status)
					if tc.sha256Status == http.StatusOK {
						fmt.Fprint(w, tc.sha256Body)
					}
					return
				}
				kernelRequested = true
				w.WriteHeader(tc.kernelStatus)
				if tc.kernelStatus == http.StatusOK {
					w.Write(tc.kernelBody)
				}
			}))
			defer srv.Close()

			t.Setenv("NEXUS3_RELEASE_BASE_URL", srv.URL)

			var stdout, stderr bytes.Buffer
			out := NewOutput(&stdout, &stderr, false)

			cmd, ok := Lookup("kernel install")
			if !ok {
				t.Fatal("kernel install not registered")
			}

			err := cmd.Run(t.Context(), []string{"--version", "1.0.0"}, out)

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
