package builder

import (
	"archive/tar"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/bootspec"
)

// ociIndex is the subset of an OCI image index (index.json) this package needs.
type ociIndex struct {
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}

// ociManifest is the subset of an OCI image manifest this package needs.
type ociManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
}

// ociImageConfigJSON is the subset of an OCI image config blob this package needs.
// The OCI spec nests process config under "config"; historical Docker images may
// use "Config" (capital C) — we parse both via separate fields and a JSON alias.
type ociImageConfigJSON struct {
	Config struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
		WorkingDir string   `json:"WorkingDir"`
		Env        []string `json:"Env"`
	} `json:"config"`
}

// parseOCIConfigFromTar reads an OCI image layout tar stream and returns the
// effective (merged) image config — the config that the built image will run
// with, including values inherited from the FROM base image.
//
// Layer blobs (large entries) are skipped via [io.Discard] without buffering;
// only the small index.json, manifest, and config blobs are retained in memory.
//
// Returns (zero, false, nil) when the OCI config is not found or not parseable
// (non-fatal; callers fall back to Containerfile-only parsing).
func parseOCIConfigFromTar(r io.Reader) (bootspec.OCIImageConfig, bool, error) {
	// Entries larger than this are layer blobs — skip their content.
	const maxSmallEntry = 4 << 20 // 4 MiB

	blobs := make(map[string][]byte)
	var indexJSON []byte

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Non-fatal: an incomplete OCI tar (e.g. solve error mid-stream) should
			// not surface as a build failure — boot.json generation is best-effort.
			return bootspec.OCIImageConfig{}, false, nil
		}

		if hdr.Size > maxSmallEntry {
			// Layer blob — discard content without buffering.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return bootspec.OCIImageConfig{}, false, nil
			}
			continue
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return bootspec.OCIImageConfig{}, false, nil
		}

		switch {
		case hdr.Name == "index.json":
			indexJSON = data
		case strings.HasPrefix(hdr.Name, "blobs/sha256/"):
			digest := strings.TrimPrefix(hdr.Name, "blobs/sha256/")
			blobs[digest] = data
		}
	}

	if indexJSON == nil {
		return bootspec.OCIImageConfig{}, false, nil
	}

	var index ociIndex
	if err := json.Unmarshal(indexJSON, &index); err != nil {
		return bootspec.OCIImageConfig{}, false, nil
	}
	if len(index.Manifests) == 0 {
		return bootspec.OCIImageConfig{}, false, nil
	}

	// Use the first manifest (single-platform builds have exactly one).
	manifestDigest := strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")
	manifestData, ok := blobs[manifestDigest]
	if !ok {
		return bootspec.OCIImageConfig{}, false, nil
	}

	var manifest ociManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return bootspec.OCIImageConfig{}, false, nil
	}

	configDigest := strings.TrimPrefix(manifest.Config.Digest, "sha256:")
	configData, ok := blobs[configDigest]
	if !ok {
		return bootspec.OCIImageConfig{}, false, nil
	}

	var raw ociImageConfigJSON
	if err := json.Unmarshal(configData, &raw); err != nil {
		return bootspec.OCIImageConfig{}, false, nil
	}

	cfg := bootspec.OCIImageConfig{
		Entrypoint: raw.Config.Entrypoint,
		Cmd:        raw.Config.Cmd,
		WorkingDir: raw.Config.WorkingDir,
		Env:        raw.Config.Env,
	}
	return cfg, true, nil
}

// captureBootSpec writes boot.json into <outDir>/etc/nexus3/boot.json.
//
// When ociCfg is non-nil it is the authoritative effective image config —
// the merged result of the built OCI image that already incorporates both
// base-image inherited values (ENTRYPOINT/CMD/WORKDIR/ENV declared in the
// FROM image) and values declared in the user's .nexus/Containerfile.
// This is the primary path: a Containerfile that has no ENTRYPOINT line but
// whose FROM base has one will produce a boot task via ociCfg.
//
// When ociCfg is nil (OCI export failed or was unavailable), the function
// falls back to [captureBootSpecFromContainerfile], which only captures
// instructions DECLARED IN THE USER'S .nexus/Containerfile (the prior
// D-DC-30 behavior). This preserves correctness for the common case and
// fail-safe behavior when the OCI export is unavailable.
//
// All failures are NON-FATAL: a missing or unwriteable boot.json must never
// fail the build (the rootfs export already succeeded).
func captureBootSpec(containerfileBytes []byte, ociCfg *bootspec.OCIImageConfig, outDir string) {
	if ociCfg != nil {
		spec := bootspec.FromOCIImageConfig(*ociCfg)
		if len(spec.Tasks) == 0 {
			slog.Debug("buildkit: captureBootSpec: OCI config has no entrypoint/cmd; no boot.json written")
			return
		}
		writeBootJSON(spec, outDir, "OCI config")
		return
	}
	// OCI config unavailable — fall back to Containerfile-only parse.
	captureBootSpecFromContainerfile(containerfileBytes, outDir)
}

// writeBootJSON marshals spec and writes it to <outDir>/etc/nexus3/boot.json.
// source is a short label for log messages. All failures are non-fatal.
func writeBootJSON(spec bootspec.Spec, outDir string, source string) {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		slog.Warn("buildkit: writeBootJSON: failed to marshal boot spec", "source", source, "err", err)
		return
	}
	bootJSONPath := filepath.Join(outDir, "etc", "nexus3", "boot.json")
	if err := os.MkdirAll(filepath.Dir(bootJSONPath), 0755); err != nil {
		slog.Warn("buildkit: writeBootJSON: failed to create parent dirs", "source", source, "err", err)
		return
	}
	if err := os.WriteFile(bootJSONPath, specJSON, 0644); err != nil {
		slog.Warn("buildkit: writeBootJSON: failed to write boot.json", "source", source, "err", err)
		return
	}
	slog.Info("buildkit: writeBootJSON: wrote boot.json", "source", source, "path", bootJSONPath, "tasks", len(spec.Tasks))
}
