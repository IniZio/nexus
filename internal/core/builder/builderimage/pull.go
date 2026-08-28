//go:build linux

package builderimage

// PullAndCacheOCI and addUserRunLayers are placed in the builderimage package
// because they reuse extractImageLayers and buildExt4, which are unexported
// helpers already defined here. Placing the function in the image package
// would require duplicating those helpers or introducing a third shared package.
// Placing it in service would push OCI/ext4 concerns into the coordination
// layer. builderimage→image is a downward, acyclic edge (image does not
// import builder).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/IniZio/nexus3/internal/core/bootspec"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// pullAmd64RemoteImage is a var so tests can override it without hitting the network.
//
// Unlike pullRemoteImage (used by EnsureBuilderImage), this variant requests
// linux/amd64 explicitly via remote.WithPlatform so that multi-arch manifests
// always resolve to the correct slice on the x86_64 host.
//
// TODO(auth): add a remote.WithAuth parameter once private-registry support is needed.
var pullAmd64RemoteImage = func(ctx context.Context, ociRef string) (v1.Image, error) {
	ref, err := name.ParseReference(ociRef)
	if err != nil {
		return nil, fmt.Errorf("parse OCI ref %q: %w", ociRef, err)
	}
	img, err := remote.Image(ref,
		remote.WithContext(ctx),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}),
	)
	if err != nil {
		return nil, fmt.Errorf("pull OCI image %q (linux/amd64): %w", ociRef, err)
	}
	return img, nil
}

// PullAndCacheOCI pulls an OCI image from a public registry (anonymous auth),
// converts it to a bootable ext4 rootfs by:
//
//  1. extracting all layers (OCI whiteout semantics),
//  2. injecting the nexus3-agent binary as /sbin/nexus3-agent (kernel init= path)
//     and /usr/local/bin/nexus3-agent, plus /etc/nexus3/boot.json derived from
//     the image's OCI config (Entrypoint/Cmd/WorkingDir/Env),
//  3. building a raw ext4 image via mke2fs,
//  4. storing the result in the image cache keyed by the SHA-256 of the ext4
//     artifact (content-addressed), with img.Ref = ociRef.
//
// A subsequent call with the same ociRef skips the pull and returns the cached
// digest immediately (ref-based hit).
//
// agentBytes must be the raw bytes of the nexus3-agent binary. The caller is
// responsible for locating it (e.g. exec.LookPath("nexus3-agent") + os.ReadFile).
//
// TODO(auth): pass credentials for private-registry pulls via remote.WithAuth.
func PullAndCacheOCI(ctx context.Context, ociRef string, c *image.Cache, agentBytes []byte) (digest string, err error) {
	if len(agentBytes) == 0 {
		return "", fmt.Errorf("ocirun: agentBytes must not be empty")
	}

	// Ref-based cache hit: if any entry already carries this ref, skip the pull.
	imgs, listErr := c.List(ctx)
	if listErr != nil {
		return "", fmt.Errorf("ocirun: list cache: %w", listErr)
	}
	for _, img := range imgs {
		if img.Ref == ociRef {
			slog.Info("ocirun: cache hit", "ref", ociRef, "digest", img.Digest)
			return string(img.Digest), nil
		}
	}

	slog.Info("ocirun: pulling OCI image (may take a moment)", "ref", ociRef)
	img, err := pullAmd64RemoteImage(ctx, ociRef)
	if err != nil {
		return "", fmt.Errorf("ocirun: pull %q: %w", ociRef, err)
	}

	stagingDir, err := os.MkdirTemp("", "nexus3-ocirun-rootfs-*")
	if err != nil {
		return "", fmt.Errorf("ocirun: staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	slog.Info("ocirun: extracting OCI layers", "ref", ociRef)
	if err := extractImageLayers(img, stagingDir); err != nil {
		return "", fmt.Errorf("ocirun: extract layers: %w", err)
	}

	slog.Info("ocirun: injecting user-run boot layers", "ref", ociRef)
	if err := addUserRunLayers(stagingDir, agentBytes, img); err != nil {
		return "", fmt.Errorf("ocirun: boot layers: %w", err)
	}

	// Build ext4 to a temp file; compute its SHA-256; commit to cache.
	tmpExt4, err := os.CreateTemp("", "nexus3-ocirun-*.ext4")
	if err != nil {
		return "", fmt.Errorf("ocirun: temp ext4: %w", err)
	}
	tmpPath := tmpExt4.Name()
	tmpExt4.Close()
	defer os.Remove(tmpPath) // cache.Put atomically renames on success; harmless no-op afterward

	slog.Info("ocirun: building ext4 image", "ref", ociRef)
	if err := buildExt4(ctx, stagingDir, tmpPath); err != nil {
		return "", fmt.Errorf("ocirun: ext4 build: %w", err)
	}

	digestStr, err := sha256SumFile(tmpPath)
	if err != nil {
		return "", fmt.Errorf("ocirun: hash ext4: %w", err)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return "", fmt.Errorf("ocirun: open ext4 for cache: %w", err)
	}
	defer f.Close()

	domainImg := domain.Image{
		Digest:    domain.Digest(digestStr),
		Ref:       ociRef,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
	}
	if putErr := c.Put(ctx, domainImg, f); putErr != nil {
		return "", fmt.Errorf("ocirun: cache put: %w", putErr)
	}

	slog.Info("ocirun: cached", "ref", ociRef, "digest", digestStr)
	return digestStr, nil
}

// addUserRunLayers injects the minimal files needed to boot a user-run OCI image
// as a nexus3 VM rootfs. It is intentionally a strict subset of addBootLayers —
// specifically it does NOT inject builder-specific files (/run/buildkit,
// /var/lib/buildkit, builder-init.sh, NEXUS_VMBUILDER=1 env) which are only
// needed by the builder VM (moby/buildkit).
//
// What it injects:
//
//  1. /sbin/nexus3-agent  — PID-1 via kernel cmdline "init=/sbin/nexus3-agent"
//     (the kernel does NOT follow symlinks for init=, so a physical copy is required)
//  2. /usr/local/bin/nexus3-agent  — conventional PATH location
//  3. /etc/nexus3/boot.json  — derived from the OCI image config (Entrypoint/Cmd/
//     WorkingDir/Env) via bootspec.FromOCIImageConfig; omitted when the image has
//     no declared process (pure dev-workspace case, matching devcontainers behaviour)
//  4. /etc/securetty — ttyS0 appended for serial console login
//  5. /etc/resolv.conf — seeded with public DNS; guest agent overwrites after boot
func addUserRunLayers(stagingDir string, agentBytes []byte, img v1.Image) error {
	// nexus3-agent binary at /sbin/ and /usr/local/bin/.
	for _, rel := range []string{"sbin/nexus3-agent", "usr/local/bin/nexus3-agent"} {
		dst := filepath.Join(stagingDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", rel, err)
		}
		_ = os.Remove(dst)
		if err := os.WriteFile(dst, agentBytes, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}

	// /etc/nexus3/boot.json from the OCI image config.
	cf, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("read OCI config file: %w", err)
	}
	spec := bootspec.FromOCIImageConfig(bootspec.OCIImageConfig{
		Entrypoint: cf.Config.Entrypoint,
		Cmd:        cf.Config.Cmd,
		WorkingDir: cf.Config.WorkingDir,
		Env:        cf.Config.Env,
	})
	if len(spec.Tasks) > 0 {
		bootDir := filepath.Join(stagingDir, "etc", "nexus3")
		if err := os.MkdirAll(bootDir, 0o755); err != nil {
			return fmt.Errorf("mkdir /etc/nexus3: %w", err)
		}
		data, err := json.Marshal(spec)
		if err != nil {
			return fmt.Errorf("marshal boot.json: %w", err)
		}
		if err := os.WriteFile(filepath.Join(bootDir, "boot.json"), data, 0o644); err != nil {
			return fmt.Errorf("write /etc/nexus3/boot.json: %w", err)
		}
	}

	// /etc/securetty — add ttyS0 for serial console login.
	securetty := filepath.Join(stagingDir, "etc", "securetty")
	if err := os.MkdirAll(filepath.Dir(securetty), 0o755); err != nil {
		return fmt.Errorf("mkdir /etc for securetty: %w", err)
	}
	existing, _ := os.ReadFile(securetty)
	if !strings.Contains(string(existing), "ttyS0") {
		f, err := os.OpenFile(securetty, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open /etc/securetty: %w", err)
		}
		if _, err := f.WriteString("ttyS0\n"); err != nil {
			f.Close()
			return fmt.Errorf("securetty: write ttyS0: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("securetty: close: %w", err)
		}
	}

	// /etc/resolv.conf — replace any symlink with a real file (some images
	// use a symlink that points to a path that only exists inside the running
	// container, not in the offline ext4 image).
	etcDir := filepath.Join(stagingDir, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir /etc: %w", err)
	}
	resolvDst := filepath.Join(etcDir, "resolv.conf")
	_ = os.Remove(resolvDst) // remove any pre-existing symlink
	const resolvContents = "# seeded by nexus3 ocirun — agent overwrites after boot\nnameserver 8.8.8.8\nnameserver 1.1.1.1\n"
	if err := os.WriteFile(resolvDst, []byte(resolvContents), 0o644); err != nil {
		return fmt.Errorf("write /etc/resolv.conf: %w", err)
	}

	return nil
}

// sha256SumFile returns the SHA-256 digest of the file at path in
// "sha256:<hex>" format, as expected by domain.Digest and image.Cache.Put.
func sha256SumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
