//go:build linux

package builderimage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/IniZio/nexus3/internal/core/bootspec"
	"github.com/IniZio/nexus3/internal/core/builder/builderimage"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// TestPullAndCacheOCI_EmptyAgentBytes verifies that nil agentBytes produces a
// clear error without touching the network.
func TestPullAndCacheOCI_EmptyAgentBytes(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	_, err = builderimage.PullAndCacheOCI(context.Background(), "alpine:3.20", c, nil)
	if err == nil {
		t.Fatal("expected error for nil agentBytes, got nil")
	}
}

// TestPullAndCacheOCI_CacheHit verifies that a call whose ref is already in
// the cache returns the existing digest without calling pullAmd64RemoteImage.
func TestPullAndCacheOCI_CacheHit(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Pre-seed the cache with an entry carrying the target ref.
	content := []byte("fake-ext4-content-for-hit-test")
	h := sha256.Sum256(content)
	existingDigest := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	existingImg := domain.Image{
		Digest:    existingDigest,
		Ref:       "alpine:3.20",
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
	}
	if err := c.Put(context.Background(), existingImg, bytes.NewReader(content)); err != nil {
		t.Fatalf("pre-seed Put: %v", err)
	}

	// Override pull to assert it is never called.
	pullCalled := false
	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCalled = true
		t.Error("pullAmd64RemoteImage called on cache hit")
		return nil, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	got, err := builderimage.PullAndCacheOCI(context.Background(), "alpine:3.20", c, fakeAgentBytes)
	if err != nil {
		t.Fatalf("PullAndCacheOCI: %v", err)
	}
	if pullCalled {
		t.Error("pull was invoked despite a cache hit")
	}
	if got != string(existingDigest) {
		t.Errorf("digest = %q, want %q", got, existingDigest)
	}
}

// TestPullAndCacheOCI_PullAndStore verifies the full pull-extract-cache path
// using a fake in-memory OCI image (no network). Requires mke2fs.
func TestPullAndCacheOCI_PullAndStore(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not in PATH")
	}

	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const testRef = "docker.io/library/alpine:testonly"
	fakeImg := buildMinimalOCIImage(t) // defined in image_test.go, same test package
	pullCount := 0

	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, ref string) (v1.Image, error) {
		if ref != testRef {
			t.Errorf("unexpected ref %q", ref)
		}
		pullCount++
		return fakeImg, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	// First call: must pull and cache.
	digest1, err := builderimage.PullAndCacheOCI(context.Background(), testRef, c, fakeAgentBytes)
	if err != nil {
		t.Fatalf("first PullAndCacheOCI: %v", err)
	}
	if pullCount != 1 {
		t.Errorf("pull count = %d, want 1 after first call", pullCount)
	}
	if digest1 == "" {
		t.Error("returned empty digest")
	}

	// Verify cache entry: ref + kind.
	imgs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *domain.Image
	for i := range imgs {
		if imgs[i].Ref == testRef {
			found = &imgs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no cache entry with ref %q", testRef)
	}
	if found.Kind != domain.KindBase {
		t.Errorf("Kind = %v, want KindBase", found.Kind)
	}
	if string(found.Digest) != digest1 {
		t.Errorf("cached digest %q != returned %q", found.Digest, digest1)
	}

	// Second call: cache hit — pull must not be invoked again.
	digest2, err := builderimage.PullAndCacheOCI(context.Background(), testRef, c, fakeAgentBytes)
	if err != nil {
		t.Fatalf("second PullAndCacheOCI: %v", err)
	}
	if pullCount != 1 {
		t.Errorf("pull count = %d, want still 1 after cache hit", pullCount)
	}
	if digest2 != digest1 {
		t.Errorf("second call digest %q != first %q", digest2, digest1)
	}
}

// buildOCIImageWithEntrypoint creates a minimal v1.Image that declares an
// Entrypoint so addUserRunLayers writes a boot.json into the staging dir.
func buildOCIImageWithEntrypoint(t *testing.T) v1.Image {
	t.Helper()
	img := buildMinimalOCIImage(t) // layer with /usr/bin/buildkitd
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	cfg.Config.Entrypoint = []string{"/usr/bin/buildkitd"}
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatalf("mutate.ConfigFile: %v", err)
	}
	return img
}

// brokenLayersImage is a v1.Image whose Layers() call always returns an error
// so that extractImageLayers fails predictably.
type brokenLayersImage struct {
	v1.Image
}

func (b brokenLayersImage) Layers() ([]v1.Layer, error) {
	return nil, errors.New("layers: intentional test failure")
}

// TestPullAndCacheOCI_Ext4Payload builds a full ext4 via the mock pipeline and
// verifies the injected payload: securetty contains "ttyS0", /sbin/nexus3-agent
// is present, and boot.json exists. Skipped when mke2fs is not in PATH.
func TestPullAndCacheOCI_Ext4Payload(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not in PATH")
	}

	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const testRef = "docker.io/library/alpine:payload-test"
	fakeImg := buildOCIImageWithEntrypoint(t)

	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		return fakeImg, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	// Build the ext4 and retrieve the path from the cache.
	// Cache layout: <root>/<algo>/<hex>/artifact  (e.g. sha256/<64hex>/artifact).
	digestStr, err := builderimage.PullAndCacheOCI(context.Background(), testRef, c, fakeAgentBytes)
	if err != nil {
		t.Fatalf("PullAndCacheOCI: %v", err)
	}

	// Derive the artifact path from the digest string "sha256:<hex>".
	parts := strings.SplitN(digestStr, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected digest format %q", digestStr)
	}
	imagePath := filepath.Join(root, parts[0], parts[1], "artifact")
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("artifact not found at %s: %v", imagePath, err)
	}

	verifyExt4HasFile(t, imagePath, "/sbin/nexus3-agent", "nexus3-agent-fake")
	verifyExt4HasFile(t, imagePath, "/etc/securetty", "ttyS0")
	verifyExt4FileExists(t, imagePath, "/etc/nexus3/boot.json")
}

// verifyExt4HasFile asserts that path exists in the ext4 and its content
// contains want. Uses debugfs cat; falls back to raw-byte scan.
func verifyExt4HasFile(t *testing.T, imagePath, filePath, want string) {
	t.Helper()
	if dbgPath, err := exec.LookPath("debugfs"); err == nil {
		out, err := exec.Command(dbgPath, "-R", "cat "+filePath, imagePath).CombinedOutput()
		if err == nil && strings.Contains(string(out), want) {
			return
		}
		// debugfs may exit non-zero if file is missing — fall through to byte scan.
	}
	// Raw fallback.
	data, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read ext4 for fallback: %v", err)
	}
	if !bytes.Contains(data, []byte(want)) {
		t.Errorf("ext4 does not contain %q (checked %s)", want, filePath)
	}
}

// verifyExt4FileExists asserts that filePath is present in the ext4 image.
func verifyExt4FileExists(t *testing.T, imagePath, filePath string) {
	t.Helper()
	if dbgPath, err := exec.LookPath("debugfs"); err == nil {
		out, _ := exec.Command(dbgPath, "-R", "stat "+filePath, imagePath).CombinedOutput()
		if strings.Contains(string(out), "Inode:") {
			return
		}
	}
	// Fallback: the filename itself should appear in the raw ext4 bytes.
	data, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read ext4 for fallback: %v", err)
	}
	base := filePath[strings.LastIndex(filePath, "/")+1:]
	if !bytes.Contains(data, []byte(base)) {
		t.Errorf("ext4 does not appear to contain file %q", filePath)
	}
}

// TestPullAndCacheOCI_PullError verifies that a pull failure wraps the error
// with "ocirun: pull". No mke2fs required.
func TestPullAndCacheOCI_PullError(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		return nil, errors.New("registry: connection refused")
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	_, err = builderimage.PullAndCacheOCI(context.Background(), "docker.io/library/alpine:pullerr", c, fakeAgentBytes)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocirun: pull") {
		t.Errorf("error %q does not contain \"ocirun: pull\"", err.Error())
	}
}

// TestPullAndCacheOCI_ExtractError verifies that a Layers() failure wraps with
// "ocirun: extract layers". No mke2fs required.
func TestPullAndCacheOCI_ExtractError(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	good := buildMinimalOCIImage(t)
	broken := brokenLayersImage{Image: good}

	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		return broken, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	_, err = builderimage.PullAndCacheOCI(context.Background(), "docker.io/library/alpine:extracterr", c, fakeAgentBytes)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocirun: extract layers") {
		t.Errorf("error %q does not contain \"ocirun: extract layers\"", err.Error())
	}
}

// TestPullAndCacheOCI_CachePutError verifies that a cache.Put failure wraps
// with "ocirun: cache put". Requires mke2fs and skipped when running as root
// (root ignores 0555 dir permissions).
func TestPullAndCacheOCI_CachePutError(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not in PATH")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; 0555 dir permission test is ineffective")
	}

	// Create a cache root that is read-only so Put's MkdirAll fails.
	readonlyRoot := t.TempDir()
	if err := os.Chmod(readonlyRoot, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(readonlyRoot, 0o755) }) // allow cleanup

	c, err := image.NewCache(readonlyRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	fakeImg := buildOCIImageWithEntrypoint(t)
	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		return fakeImg, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	_, err = builderimage.PullAndCacheOCI(context.Background(), "docker.io/library/alpine:puterr", c, fakeAgentBytes)
	if err == nil {
		t.Fatal("expected error from cache put, got nil")
	}
	if !strings.Contains(err.Error(), "ocirun: cache put") {
		t.Errorf("error %q does not contain \"ocirun: cache put\"", err.Error())
	}
}

// TestAddUserRunLayers_BootspecFromOCIConfig is a pure-logic unit test for
// the OCI image config → bootspec.Spec translation that addUserRunLayers uses.
// It does not require mke2fs or a real OCI image.
func TestAddUserRunLayers_BootspecFromOCIConfig(t *testing.T) {
	cases := []struct {
		name      string
		cfg       bootspec.OCIImageConfig
		wantTasks int
		wantArgv  []string
	}{
		{
			name: "entrypoint+cmd",
			cfg: bootspec.OCIImageConfig{
				Entrypoint: []string{"/usr/bin/python3"},
				Cmd:        []string{"-m", "http.server", "8080"},
				WorkingDir: "/app",
			},
			wantTasks: 1,
			wantArgv:  []string{"/usr/bin/python3", "-m", "http.server", "8080"},
		},
		{
			name:      "no entrypoint or cmd -> no boot task",
			cfg:       bootspec.OCIImageConfig{},
			wantTasks: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := bootspec.FromOCIImageConfig(tc.cfg)
			if len(spec.Tasks) != tc.wantTasks {
				t.Fatalf("tasks = %d, want %d", len(spec.Tasks), tc.wantTasks)
			}
			if tc.wantTasks == 0 {
				return
			}
			if len(spec.Tasks[0].Argv) != len(tc.wantArgv) {
				t.Fatalf("argv = %v, want %v", spec.Tasks[0].Argv, tc.wantArgv)
			}
			for i, a := range tc.wantArgv {
				if spec.Tasks[0].Argv[i] != a {
					t.Errorf("argv[%d] = %q, want %q", i, spec.Tasks[0].Argv[i], a)
				}
			}
			if !spec.Tasks[0].Background {
				t.Error("expected Background=true for user-run task (agent is PID 1)")
			}

			// JSON round-trip.
			data, err := json.Marshal(spec)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded bootspec.Spec
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(decoded.Tasks) != tc.wantTasks {
				t.Errorf("round-trip tasks = %d, want %d", len(decoded.Tasks), tc.wantTasks)
			}
		})
	}
}
