//go:build linux

package builderimage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

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
