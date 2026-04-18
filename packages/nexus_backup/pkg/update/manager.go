package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/daemonclient"
)

var (
	resolveBinaryPathsFn    = resolveBinaryPaths
	restartAndProbeDaemonFn = restartAndProbeDaemon
	stopRunningDaemonFn     = stopRunningDaemon
)

func Check(ctx context.Context, opts Options) (Status, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	state, err := readState(paths.StateFile)
	if err != nil {
		return Status{}, err
	}
	manifest, err := fetchManifest(ctx, opts)
	if err != nil {
		return Status{
			CurrentVersion: opts.CurrentVersion,
			LastCheckedAt:  state.LastCheckedAt,
			LastFailureAt:  state.LastFailureAt,
			LastFailure:    err.Error(),
		}, err
	}
	now := time.Now().UTC()
	state.LastCheckedAt = now.Format(time.RFC3339)
	status := Status{
		CurrentVersion: opts.CurrentVersion,
		LatestVersion:  manifest.Version,
		LastCheckedAt:  state.LastCheckedAt,
		LastSuccessAt:  state.LastSuccessAt,
		LastFailureAt:  state.LastFailureAt,
		LastFailure:    state.LastFailure,
		UpdateReady:    compareVersion(opts.CurrentVersion, manifest.Version) < 0,
		CheckedAt:      &now,
	}
	_ = writeState(paths.StateFile, state)
	return status, nil
}

func AutoUpdate(ctx context.Context, opts Options) (Result, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return Result{}, err
	}
	lock, err := acquireLock(paths.LockFile)
	if err != nil {
		return Result{}, nil
	}
	defer lock.Close()

	state, err := readState(paths.StateFile)
	if err != nil {
		return Result{}, err
	}
	if !shouldCheck(state, opts.CheckInterval, opts.Force) {
		return Result{}, nil
	}
	manifest, err := fetchManifest(ctx, opts)
	if err != nil {
		state.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
		state.LastFailureAt = state.LastCheckedAt
		state.LastFailure = err.Error()
		_ = writeState(paths.StateFile, state)
		return Result{}, err
	}
	state.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	if compareVersion(opts.CurrentVersion, manifest.Version) >= 0 {
		state.CurrentVersion = opts.CurrentVersion
		_ = writeState(paths.StateFile, state)
		return Result{}, nil
	}
	if shouldSuppressVersion(state, manifest.Version, opts.BadVersionCooldown) {
		return Result{}, nil
	}
	if compareVersion(opts.CurrentUpdater, manifest.MinimumUpdaterVersion) < 0 {
		return Result{}, fmt.Errorf("updater version %s does not satisfy minimum %s", opts.CurrentUpdater, manifest.MinimumUpdaterVersion)
	}
	if !opts.AutoApply {
		_ = writeState(paths.StateFile, state)
		return Result{}, nil
	}
	result, err := applyUpdate(ctx, paths, state, manifest, opts)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func ForceUpdate(ctx context.Context, opts Options) (Result, error) {
	opts.Force = true
	opts.AutoApply = true
	return AutoUpdate(ctx, opts)
}

func Rollback(ctx context.Context) error {
	_ = ctx
	paths, err := ResolvePaths()
	if err != nil {
		return err
	}
	cliPath, daemonPath, err := resolveBinaryPathsFn()
	if err != nil {
		return err
	}
	cliBackup := filepath.Join(paths.BackupDir, filepath.Base(cliPath))
	daemonBackup := filepath.Join(paths.BackupDir, filepath.Base(daemonPath))
	if _, err := os.Stat(cliBackup); err != nil {
		return err
	}
	if _, err := os.Stat(daemonBackup); err != nil {
		return err
	}
	if err := replaceBinary(cliBackup, cliPath); err != nil {
		return err
	}
	if err := replaceBinary(daemonBackup, daemonPath); err != nil {
		return err
	}
	port := daemonPort()
	_ = stopRunningDaemonFn(port)
	if token, tokenErr := daemonToken(); tokenErr == nil {
		_ = daemonclient.EnsureRunning(port, "", token)
	}
	return nil
}

func applyUpdate(ctx context.Context, paths Paths, state State, manifest ReleaseManifest, opts Options) (Result, error) {
	target := manifest.Artifacts[currentTargetKey()]
	if strings.TrimSpace(target.CLI.Name) == "" || strings.TrimSpace(target.Daemon.Name) == "" {
		return Result{}, fmt.Errorf("missing artifacts for target %s", currentTargetKey())
	}
	if err := os.MkdirAll(paths.StagedDir, 0o755); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(paths.BackupDir, 0o755); err != nil {
		return Result{}, err
	}
	cliPath, daemonPath, err := resolveBinaryPathsFn()
	if err != nil {
		return Result{}, err
	}
	cliStaged, err := downloadArtifact(ctx, paths.StagedDir, target.URLBase, target.CLI)
	if err != nil {
		state.AttemptedVersion = manifest.Version
		state.LastFailureAt = time.Now().UTC().Format(time.RFC3339)
		state.LastFailure = err.Error()
		markBadVersion(&state, manifest.Version)
		_ = writeState(paths.StateFile, state)
		return Result{}, err
	}
	daemonStaged, err := downloadArtifact(ctx, paths.StagedDir, target.URLBase, target.Daemon)
	if err != nil {
		state.AttemptedVersion = manifest.Version
		state.LastFailureAt = time.Now().UTC().Format(time.RFC3339)
		state.LastFailure = err.Error()
		markBadVersion(&state, manifest.Version)
		_ = writeState(paths.StateFile, state)
		return Result{}, err
	}
	if err := validateStagedBinary(cliStaged, "version", "--json"); err != nil {
		state.AttemptedVersion = manifest.Version
		state.LastFailureAt = time.Now().UTC().Format(time.RFC3339)
		state.LastFailure = err.Error()
		markBadVersion(&state, manifest.Version)
		_ = writeState(paths.StateFile, state)
		return Result{}, err
	}
	if err := validateStagedBinary(daemonStaged, "--help"); err != nil {
		state.AttemptedVersion = manifest.Version
		state.LastFailureAt = time.Now().UTC().Format(time.RFC3339)
		state.LastFailure = err.Error()
		markBadVersion(&state, manifest.Version)
		_ = writeState(paths.StateFile, state)
		return Result{}, err
	}
	cliBackup := filepath.Join(paths.BackupDir, filepath.Base(cliPath))
	daemonBackup := filepath.Join(paths.BackupDir, filepath.Base(daemonPath))
	if err := copyFile(cliPath, cliBackup); err != nil {
		return Result{}, err
	}
	if err := copyFile(daemonPath, daemonBackup); err != nil {
		return Result{}, err
	}
	if err := replaceBinary(cliStaged, cliPath); err != nil {
		return Result{}, err
	}
	if err := replaceBinary(daemonStaged, daemonPath); err != nil {
		_ = replaceBinary(cliBackup, cliPath)
		return Result{}, err
	}
	healthy, healthErr := restartAndProbeDaemonFn(ctx, manifest.Version)
	if healthErr != nil || !healthy {
		_ = replaceBinary(cliBackup, cliPath)
		_ = replaceBinary(daemonBackup, daemonPath)
		_, _ = restartAndProbeDaemonFn(ctx, opts.CurrentVersion)
		state.AttemptedVersion = manifest.Version
		state.LastFailureAt = time.Now().UTC().Format(time.RFC3339)
		if healthErr != nil {
			state.LastFailure = healthErr.Error()
		} else {
			state.LastFailure = "daemon healthcheck failed after update"
		}
		markBadVersion(&state, manifest.Version)
		_ = writeState(paths.StateFile, state)
		return Result{}, fmt.Errorf(state.LastFailure)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	state.LastSuccessAt = now
	state.LastSuccess = manifest.Version
	state.CurrentVersion = manifest.Version
	state.AttemptedVersion = manifest.Version
	state.LastFailure = ""
	_ = writeState(paths.StateFile, state)
	return Result{
		Updated:       true,
		FromVersion:   opts.CurrentVersion,
		ToVersion:     manifest.Version,
		DaemonHealthy: true,
	}, nil
}

func resolveBinaryPaths() (string, string, error) {
	cliExe, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	cliExe, err = filepath.EvalSymlinks(cliExe)
	if err != nil {
		return "", "", err
	}
	daemonCandidate := filepath.Join(filepath.Dir(cliExe), "nexus-daemon")
	if _, err := os.Stat(daemonCandidate); err == nil {
		return cliExe, daemonCandidate, nil
	}
	daemonPath, err := exec.LookPath("nexus-daemon")
	if err != nil {
		return "", "", err
	}
	daemonPath, err = filepath.EvalSymlinks(daemonPath)
	if err != nil {
		return "", "", err
	}
	return cliExe, daemonPath, nil
}

func downloadArtifact(ctx context.Context, stagedDir, baseURL string, artifact BinaryArtifact) (string, error) {
	artifactURL, err := buildArtifactURL(baseURL, artifact.Name)
	if err != nil {
		return "", err
	}
	body, err := fetchBytes(ctx, artifactURL)
	if err != nil {
		return "", err
	}
	if sha256Bytes(body) != strings.ToLower(strings.TrimSpace(artifact.SHA256)) {
		return "", fmt.Errorf("checksum mismatch for %s", artifact.Name)
	}
	path := filepath.Join(stagedDir, artifact.Name)
	if err := os.WriteFile(path, body, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func replaceBinary(source, destination string) error {
	tempDestination := destination + ".new"
	if err := copyFile(source, tempDestination); err != nil {
		return err
	}
	if err := os.Chmod(tempDestination, 0o755); err != nil {
		return err
	}
	return os.Rename(tempDestination, destination)
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o755)
}

func validateStagedBinary(path string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("staged binary validation failed for %s: %w", filepath.Base(path), err)
	}
	return nil
}

func restartAndProbeDaemon(ctx context.Context, expectedVersion string) (bool, error) {
	port := daemonPort()
	token, err := daemonToken()
	if err != nil {
		return false, err
	}
	_ = stopRunningDaemonFn(port)
	if err := daemonclient.EnsureRunning(port, "", token); err != nil {
		return false, err
	}
	healthURL := "http://localhost:" + strconv.Itoa(port) + "/healthz"
	versionURL := "http://localhost:" + strconv.Itoa(port) + "/version"
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			versionReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
			versionResp, versionErr := http.DefaultClient.Do(versionReq)
			if versionErr == nil && versionResp.StatusCode == http.StatusOK {
				var body struct {
					Version string `json:"version"`
				}
				decodeErr := json.NewDecoder(versionResp.Body).Decode(&body)
				_ = versionResp.Body.Close()
				if decodeErr == nil && (expectedVersion == "" || strings.TrimPrefix(body.Version, "v") == strings.TrimPrefix(expectedVersion, "v")) {
					return true, nil
				}
			}
			return true, nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false, fmt.Errorf("daemon healthcheck timeout after update")
}

func daemonPort() int {
	port := 7874
	if raw := strings.TrimSpace(os.Getenv("NEXUS_DAEMON_PORT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed > 0 {
			port = parsed
		}
	}
	return port
}

func daemonToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv("NEXUS_DAEMON_TOKEN")); token != "" {
		return token, nil
	}
	return daemonclient.ReadDaemonToken()
}

func stopRunningDaemon(port int) error {
	runDir, err := daemonclient.RunDir()
	if err != nil {
		return err
	}
	pidPath := filepath.Join(runDir, fmt.Sprintf("daemon-%d.pid", port))
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		pidData, err = os.ReadFile(filepath.Join(runDir, "daemon.pid"))
		if err != nil {
			return nil
		}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
		return nil
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if !daemonclient.IsRunning(port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = process.Signal(syscall.SIGKILL)
	return nil
}
