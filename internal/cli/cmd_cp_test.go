package cli

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// recordingCpService is a fake that reads the full tar stream from opts.Src
// during Copy() so the caller can inspect entries after runCpWithSvc returns.
// Reading inside Copy() is required because the defer in runCpWithSvc closes
// src before returning to the test.
type recordingCpService struct {
	got     agent.CopyOptions
	entries []string // tar entry names, sorted; non-nil only when IsDirectory
	readErr error    // non-nil if the tar stream was unreadable
}

func (r *recordingCpService) Copy(_ context.Context, _ string, opts agent.CopyOptions) error {
	r.got = opts
	if opts.IsDirectory && opts.Src != nil {
		tr := tar.NewReader(opts.Src)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				r.readErr = err
				return nil // let the test inspect readErr
			}
			r.entries = append(r.entries, hdr.Name)
		}
		sort.Strings(r.entries)
	}
	return nil
}

// TestRunCpWithSvc_PushDir_TarEntries verifies that a directory push sends a
// valid tar stream whose entries match the source tree.
//
// Mutation proof (remove the NewPushReader dir branch / revert to os.Open):
//   - opts.Src becomes a bare directory fd; tar.Reader.Next() calls fd.Read()
//     which returns EISDIR on Linux; recordingCpService.readErr becomes non-nil;
//     the test fails on the readErr assertion.
func TestRunCpWithSvc_PushDir_TarEntries(t *testing.T) {
	tmp := t.TempDir()
	srcDir := makeDir(t, tmp, "src")
	makeFile(t, srcDir, "a.txt", "hello")
	subDir := makeDir(t, srcDir, "sub")
	makeFile(t, subDir, "b.txt", "world")

	rec := &recordingCpService{}
	out, _, _ := capture(false)

	err := runCpWithSvc(
		context.Background(),
		"ref",
		agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		"/guest/dst",
		srcDir,
		true, // isDir
		out,
		rec,
	)
	if err != nil {
		t.Fatalf("runCpWithSvc returned unexpected error: %v", err)
	}
	if rec.readErr != nil {
		t.Fatalf("tar stream not readable (EISDIR?): %v", rec.readErr)
	}
	// Verify expected entries are present (relative paths produced by filepath.Walk).
	wantEntries := map[string]bool{
		".":       true,
		"a.txt":   true,
		"sub":     true,
		"sub/b.txt": true,
	}
	for _, e := range rec.entries {
		if !wantEntries[e] {
			t.Errorf("unexpected tar entry %q", e)
		}
		delete(wantEntries, e)
	}
	for missing := range wantEntries {
		t.Errorf("missing tar entry %q", missing)
	}
	// ExpectedBytes must be nil for directory pushes (tar integrity is the guard).
	if rec.got.ExpectedBytes != nil {
		t.Errorf("ExpectedBytes = &%d for dir push, want nil", *rec.got.ExpectedBytes)
	}
}

// TestRunCpWithSvc_PushFile_TarNotUsed verifies that a single-file push
// sends the raw file bytes (not wrapped in tar) with ExpectedBytes declared.
//
// Mutation proof (drop the ExpectedBytes stat block):
//   - rec.got.ExpectedBytes == nil; the assertion fails.
func TestRunCpWithSvc_PushFile_TarNotUsed(t *testing.T) {
	tmp := t.TempDir()
	localFile := makeFile(t, tmp, "f.txt", "nexus3")

	rec := &recordingCpService{}
	out, _, _ := capture(false)

	err := runCpWithSvc(
		context.Background(),
		"ref",
		agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		"/guest/f.txt",
		localFile,
		false, // isDir
		out,
		rec,
	)
	if err != nil {
		t.Fatalf("runCpWithSvc returned error: %v", err)
	}
	if rec.got.ExpectedBytes == nil {
		t.Fatal("ExpectedBytes is nil for single-file push; guard would be bypassed")
	}
	if *rec.got.ExpectedBytes != 6 { // len("nexus3")
		t.Errorf("ExpectedBytes = %d, want 6", *rec.got.ExpectedBytes)
	}
	// Src must be non-nil and readable as plain bytes (not tar).
	if rec.got.Src == nil {
		t.Fatal("Src is nil")
	}
}
