package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

// fakeCpService captures the CopyOptions passed to it and returns no error.
type fakeCpService struct {
	got agent.CopyOptions
}

func (f *fakeCpService) Copy(_ context.Context, _ string, opts agent.CopyOptions) error {
	f.got = opts
	return nil
}

// makeFile creates a file with the given content in dir and returns its path.
func makeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("makeFile %s: %v", name, err)
	}
	return p
}

// makeDir creates a sub-directory inside dir and returns its path.
func makeDir(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("makeDir %s: %v", name, err)
	}
	return p
}

func TestRunCpWithSvc_Push_ExpectedBytes(t *testing.T) {
	tmp := t.TempDir()

	tests := []struct {
		name          string
		fileContent   string
		isDir         bool
		wantExpNil    bool
		wantExpBytes  int64
		wantIsDir     bool
	}{
		{
			name:         "regular file 5 bytes",
			fileContent:  "hello",
			isDir:        false,
			wantExpNil:   false,
			wantExpBytes: 5,
			wantIsDir:    false,
		},
		{
			name:         "empty file",
			fileContent:  "",
			isDir:        false,
			wantExpNil:   false,
			wantExpBytes: 0,
			wantIsDir:    false,
		},
		{
			name:      "directory push",
			isDir:     true,
			wantExpNil: true,
			wantIsDir:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var localPath string
			if tc.isDir {
				localPath = makeDir(t, tmp, tc.name)
			} else {
				localPath = makeFile(t, tmp, tc.name, tc.fileContent)
			}

			fake := &fakeCpService{}
			out, _, _ := capture(false)

			err := runCpWithSvc(
				context.Background(),
				"sandbox-ref",
				agentpb.CopyDirection_COPY_DIRECTION_PUSH,
				"/guest/path",
				localPath,
				tc.isDir,
				out,
				fake,
			)
			if err != nil {
				t.Fatalf("runCpWithSvc returned unexpected error: %v", err)
			}

			got := fake.got

			// Assert IsDirectory propagated correctly.
			if got.IsDirectory != tc.wantIsDir {
				t.Errorf("IsDirectory = %v, want %v", got.IsDirectory, tc.wantIsDir)
			}

			// Assert ExpectedBytes.
			if tc.wantExpNil {
				if got.ExpectedBytes != nil {
					t.Errorf("ExpectedBytes = &%d, want nil", *got.ExpectedBytes)
				}
			} else {
				if got.ExpectedBytes == nil {
					t.Errorf("ExpectedBytes = nil, want &%d", tc.wantExpBytes)
				} else if *got.ExpectedBytes != tc.wantExpBytes {
					t.Errorf("ExpectedBytes = %d, want %d", *got.ExpectedBytes, tc.wantExpBytes)
				}
			}

			// Assert Src is non-nil for push (a future refactor that stats one
			// path but opens another would cause the underlying file to differ).
			if got.Src == nil {
				t.Errorf("Src is nil; expected an open file reader for PUSH")
			}
		})
	}
}
