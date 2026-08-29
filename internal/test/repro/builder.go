// Package repro provides a Go e2e reproduction harness for the intermittent
// buildkit 32 MiB truncation bug. Each phase lives in its own file so new
// phases (concurrency, disk-pressure) can be added without editing this file.
package repro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cacheHitThreshold is the minimum elapsed time for a build to be considered
// a genuine run through the builder VM (not a nexus3 image-cache hit).
//
// Measured values (2026-08-29, this host):
//   nexus3 FP-cache HIT (build-cache: hit — skipping builder VM):  5.2s
//   genuine build, COPY layer cached (only export+mke2fs run fresh): 30s
//
// The 20s threshold was chosen to sit in the 5–30 s gap:
//   - Any run < 20s is a cache hit (skipped builder VM)
//   - Any run ≥ 20s booted the builder VM and ran the export path
//
// The previous threshold was 45s, calibrated for the bash harness that always
// ran a fresh 450 MiB COPY (uncached) on each iteration. After switching to
// a per-run sentinel outside testfiles/ (workspace/.nexus/build-uid), the
// buildkit COPY layer is cached after the first run — only the export+mke2fs
// steps run fresh — so genuine builds now complete in 20–35s.
//
// If the buildkit layer cache is cold (first run or cache evicted), expect
// 50–90s for the full COPY+export+mke2fs path. That also passes the gate.
const cacheHitThreshold = 20 * time.Second

// BuildConfig holds configuration for one build run.
type BuildConfig struct {
	// Nexus3 is the path to the nexus3 binary. Defaults to "nexus3" on PATH.
	Nexus3 string
	// Workspace is the directory containing .nexus/Containerfile and testfiles/.
	Workspace string
	// Project is the nexus3 project name (e.g. "repro").
	Project string
	// SandboxName is the sandbox name within the project.
	SandboxName string
	// ImageStore is the path to ~/.local/state/nexus3/images/sha256.
	ImageStore string
	// AgentBin is the host nexus3-agent binary path (may be empty).
	AgentBin string
	// ElfSize is the expected byte size for file_elf (0 = unknown).
	ElfSize int64
	// ExpectedHashes maps file names to their expected sha256 digests.
	ExpectedHashes map[string]string
	// BuildTimeout caps the build. Default: 25 min.
	BuildTimeout time.Duration
	// AllowedSandboxHandles is the set of repro/* sandbox handles that this build
	// is allowed to coexist with. If nil, only the build's own handle is allowed.
	// Use this for the concurrency phase where N builds run simultaneously.
	AllowedSandboxHandles map[string]struct{}
	// LogsDir is where per-run build logs are written.
	LogsDir string
	// BuilderMemoryMiB overrides the builder VM guest RAM (--builder-memory).
	// Zero means use the nexus3 default (8192 MiB). Set this to 4096 on hosts
	// where swap is exhausted and 8 GiB is not reliably available.
	BuilderMemoryMiB uint16
}

func (c *BuildConfig) nexus3Bin() string {
	if c.Nexus3 != "" {
		return c.Nexus3
	}
	return "nexus3"
}

func (c *BuildConfig) buildTimeout() time.Duration {
	if c.BuildTimeout > 0 {
		return c.BuildTimeout
	}
	return 25 * time.Minute
}

// imageListOutput is the JSON shape of `nexus3 --json image ls`.
type imageListOutput struct {
	Data struct {
		Images []struct {
			Digest string `json:"digest"`
		} `json:"images"`
	} `json:"data"`
}

// listDigests returns sorted image digests from `nexus3 --json image ls`.
// Never returns (nil, nil) — a command failure always returns a non-nil error.
func listDigests(nexus3Bin string) ([]string, error) {
	out, err := exec.Command(nexus3Bin, "--json", "image", "ls").Output()
	if err != nil {
		return nil, fmt.Errorf("nexus3 image ls: %w", err)
	}
	var parsed imageListOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("nexus3 image ls: parse JSON: %w", err)
	}
	digests := make([]string, 0, len(parsed.Data.Images))
	for _, img := range parsed.Data.Images {
		if img.Digest != "" {
			digests = append(digests, img.Digest)
		}
	}
	sort.Strings(digests)
	return digests, nil
}

// diffDigests returns elements in after that are not in before.
func diffDigests(before, after []string) []string {
	bset := make(map[string]struct{}, len(before))
	for _, d := range before {
		bset[d] = struct{}{}
	}
	var news []string
	for _, d := range after {
		if _, seen := bset[d]; !seen {
			news = append(news, d)
		}
	}
	return news
}

// writeReproUID writes a unique sentinel to workspace/.nexus/build-uid.
//
// Sentinel placement: .nexus/build-uid instead of testfiles/.repro-uid.
//
// The nexus3 build fingerprint (FP) is computed from ALL workspace files
// (relpath, size, mtime). Writing any workspace file therefore changes the FP
// and forces nexus3 to run a fresh solve every time — which is what we want
// (bypass the nexus3 image cache so every run exercises the export path).
//
// The buildkit COPY layer cache key, however, is computed from the SOURCE
// files matching the COPY instruction ("COPY testfiles/ /testfiles/"). Files
// outside testfiles/ — including .nexus/build-uid — do not affect this key.
// So after the first run seeds the buildkit content store with the 450 MB
// testfiles/, every subsequent run hits the COPY cache (cheap: no re-copy of
// 450 MB), while still forcing a fresh export → rootfs → mke2fs path where
// the truncation bug manifests.
//
// The old approach (testfiles/.repro-uid) changed the buildkit COPY key on
// every run, requiring a full 450 MB re-copy each time. On hosts with swap
// fully exhausted the resulting I/O pressure caused the builder VM to be
// killed mid-solve (host OOM → cloud-hypervisor killed → vsock EOF).
// minPreconditionFreeGiB is the host free-space floor checked before each build.
// nexus3's own preflight requires 15 GiB; we add a 5 GiB buffer to catch
// near-exhaustion that won't trigger the nexus3 preflight but can cause
// ENOSPC during mke2fs packing or buildkit layer I/O.
const minPreconditionFreeGiB = 20

// hostDiskFreeGiB returns the available (not just free) bytes on the partition
// containing path, divided by 2^30.
func hostDiskFreeGiB(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	// Bavail = blocks available to unprivileged users (more conservative than Bfree).
	avail := float64(st.Bavail) * float64(st.Bsize)
	return avail / (1 << 30), nil
}

func writeReproUID(workspace, label string) (string, error) {
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	uid := runID + "-" + label + "\n"
	p := filepath.Join(workspace, ".nexus", "build-uid")
	return runID, os.WriteFile(p, []byte(uid), 0644)
}

// injectRunIDIntoContainerfile rewrites the Containerfile at containerfilePath,
// removing any stale "RUN echo ... > /.repro-run-id" line and appending a fresh one.
func injectRunIDIntoContainerfile(containerfilePath, runID string) error {
	data, err := os.ReadFile(containerfilePath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	// Filter stale run-id injection lines and trailing blank lines.
	var filtered []string
	for _, l := range lines {
		if strings.HasPrefix(l, "RUN echo ") && strings.Contains(l, "/.repro-run-id") {
			continue
		}
		filtered = append(filtered, l)
	}
	// Trim trailing blank lines.
	for len(filtered) > 0 && filtered[len(filtered)-1] == "" {
		filtered = filtered[:len(filtered)-1]
	}
	// Append new run-id line and trailing empty string (trailing newline).
	filtered = append(filtered, "RUN echo "+runID+" > /.repro-run-id")
	filtered = append(filtered, "")
	return os.WriteFile(containerfilePath, []byte(strings.Join(filtered, "\n")), 0644)
}

// BuildResult holds the outcome of one build attempt.
type BuildResult struct {
	Label         string
	BuildLog      string        // path to log file
	NewDigest     string        // sha256:... of new image
	ImageFile     string        // path to ext4 artifact
	Elapsed       time.Duration
	BuildExitCode int
	HostDiskFreeGiB float64   // available GiB on host at build-start (for diagnosis)
	RunID         string      // out-of-band run identifier injected into Containerfile
}

// RunBuild executes one nexus3 build and applies the cache-miss gate.
//
// Returns:
//   - (result, nil, nil)    — build ran, cache-miss gate passed; result.ImageFile is set
//   - (result, &hif, nil)   — cache-miss gate fired or build failed; hif is the HIF probe
//   - (result, nil, err)    — infrastructure error (log dir creation etc.)
//
// A probe that fires the cache-miss gate is always HarnessIntegrityFailure —
// it is never NoTruncationObserved.
func RunBuild(ctx context.Context, cfg BuildConfig, label string) (BuildResult, *ProbeResult, error) {
	nx := cfg.nexus3Bin()

	// 0a. Builder-sharing guard: ensure no other repro/* sandbox or builder VM
	// is running before we start. Waits up to 15 min, then HIF.
	sandboxRef := cfg.Project + "/" + cfg.SandboxName
	allowedHandles := cfg.AllowedSandboxHandles
	if len(allowedHandles) == 0 {
		allowedHandles = map[string]struct{}{sandboxRef: {}}
	}
	if hif := waitForBuilderFree(ctx, nx, allowedHandles); hif != nil {
		return BuildResult{Label: label}, hif, nil
	}

	// 0b. Disk-space precondition: check host free space before starting the build.
	// Zero/corrupt files in the ext4 artifact are the signature of ENOSPC during
	// mke2fs packing (or during buildkit layer I/O). A low-space HIF here surfaces
	// the environment failure and prevents it from masquerading as a truncation event.
	freeGiB, diskErr := hostDiskFreeGiB("/")
	if diskErr != nil {
		// Stat failure is unusual; report but do not abort.
		fmt.Printf("[repro] WARN: cannot check host disk space: %v\n", diskErr)
	} else if freeGiB < minPreconditionFreeGiB {
		hif := probeHIF("precondition.disk_space",
			fmt.Sprintf("ENOSPC_RISK: only %.1f GiB free on host (need ≥%d GiB); build aborted to prevent false verdicts",
				freeGiB, minPreconditionFreeGiB))
		return BuildResult{Label: label, HostDiskFreeGiB: freeGiB}, &hif, nil
	}

	// 1. List images before build (cache-miss gate: before-only-dead guard).
	before, err := listDigests(nx)
	if err != nil {
		hif := probeHIF("builder.image_list_before",
			fmt.Sprintf("IMAGE_LIST_FAILED before build: %v", err))
		return BuildResult{Label: label}, &hif, nil
	}

	// 2. Write repro-uid to bust the COPY layer cache.
	runID, err := writeReproUID(cfg.Workspace, label)
	if err != nil {
		hif := probeHIF("builder.repro_uid",
			fmt.Sprintf("failed to write repro-uid: %v", err))
		return BuildResult{Label: label}, &hif, nil
	}
	if err := injectRunIDIntoContainerfile(filepath.Join(cfg.Workspace, ".nexus", "Containerfile"), runID); err != nil {
		hif := probeHIF("builder.repro_uid",
			fmt.Sprintf("failed to inject run-id into Containerfile: %v", err))
		return BuildResult{Label: label}, &hif, nil
	}

	// 3. Create log file.
	if err := os.MkdirAll(cfg.LogsDir, 0755); err != nil {
		return BuildResult{Label: label}, nil,
			fmt.Errorf("mkdir %s: %w", cfg.LogsDir, err)
	}
	logFile := filepath.Join(cfg.LogsDir, label+"-"+runID+".log")
	lf, err := os.Create(logFile)
	if err != nil {
		return BuildResult{Label: label}, nil,
			fmt.Errorf("create log %s: %w", logFile, err)
	}
	defer lf.Close()

	// 4. Run build with timeout.
	// (sandboxRef already declared at top of function for the sharing guard)

	// Remove any pre-existing sandbox so a prior failed run doesn't block this one.
	// sandbox rm exits 1 when the sandbox is not found — that is expected; ignore it.
	// --force is NOT a valid flag for sandbox rm; omit it.
	if out, err := exec.Command(nx, "sandbox", "rm", sandboxRef).CombinedOutput(); err == nil {
		fmt.Printf("[repro] cleaned up sandbox %s\n", sandboxRef)
	} else {
		_ = out // sandbox may not exist; ignore error
	}
	// Ensure sandbox is removed on every exit path.
	defer func() {
		exec.Command(nx, "sandbox", "rm", sandboxRef).Run() //nolint:errcheck
		fmt.Printf("[repro] cleaned up sandbox %s (deferred)\n", sandboxRef)
	}()

	bctx, cancel := context.WithTimeout(ctx, cfg.buildTimeout())
	defer cancel()

	t0 := time.Now()
	createArgs := []string{"create", sandboxRef, "--file", cfg.Workspace, "--no-user-mounts"}
	if cfg.BuilderMemoryMiB > 0 {
		createArgs = append(createArgs, "--builder-memory", strconv.Itoa(int(cfg.BuilderMemoryMiB)))
	}
	cmd := exec.CommandContext(bctx, nx, createArgs...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	buildErr := cmd.Run()
	elapsed := time.Since(t0)

	exitCode := 0
	if buildErr != nil {
		if bctx.Err() == context.DeadlineExceeded {
			exitCode = 124 // timeout sentinel matching bash convention
		} else if ex, ok := buildErr.(*exec.ExitError); ok {
			exitCode = ex.ExitCode()
		} else {
			exitCode = -1
		}
	}

	result := BuildResult{
		Label:           label,
		BuildLog:        logFile,
		Elapsed:         elapsed,
		BuildExitCode:   exitCode,
		HostDiskFreeGiB: freeGiB,
		RunID:           runID,
	}

	// 5. Check timeout.
	if exitCode == 124 {
		hif := probeHIF("builder.timeout",
			fmt.Sprintf("build timed out after %v", elapsed.Round(time.Second)))
		return result, &hif, nil
	}

	// 6. Check build failure.
	if exitCode != 0 {
		hif := probeHIF("builder.build_failed",
			fmt.Sprintf("build exited %d after %v", exitCode, elapsed.Round(time.Second)))
		return result, &hif, nil
	}

	// 7. Cache-miss gate: successful build must have taken ≥45 s.
	// A build completing faster almost certainly hit the layer cache and skipped
	// the snapshotter materialisation path — that path is where the truncation
	// bug lives.  Reporting NoTruncationObserved on a cache-hit build is the
	// original false-verdict bug from the bash harness sessions.
	if elapsed < cacheHitThreshold {
		hif := probeHIF("builder.cache_miss_gate",
			fmt.Sprintf("CACHE_HIT_SUSPECTED: build completed in %v (need >%v for genuine cache miss)",
				elapsed.Round(time.Second), cacheHitThreshold))
		return result, &hif, nil
	}

	// 8. Find new image digest (cache-miss gate: no-new-image guard).
	after, err := listDigests(nx)
	if err != nil {
		hif := probeHIF("builder.image_list_after",
			fmt.Sprintf("IMAGE_LIST_FAILED after build: %v", err))
		return result, &hif, nil
	}

	news := diffDigests(before, after)
	if len(news) == 0 {
		hif := probeHIF("builder.no_new_image",
			"NO_NEW_IMAGE: no new digest after build (build-cache hit?)")
		return result, &hif, nil
	}

	digest := news[0]
	short := strings.TrimPrefix(digest, "sha256:")
	imgFile := filepath.Join(cfg.ImageStore, short, "artifact")
	if _, err := os.Stat(imgFile); err != nil {
		hif := probeHIF("builder.image_file_missing",
			fmt.Sprintf("IMAGE_FILE_MISSING: %s: %v", imgFile, err))
		return result, &hif, nil
	}

	result.NewDigest = digest
	result.ImageFile = imgFile
	return result, nil, nil
}
