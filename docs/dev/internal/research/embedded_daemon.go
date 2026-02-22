// Package mutagen provides embedded Mutagen daemon integration for Nexus.
//
// This package implements the embedded Mutagen daemon approach, where the
// Mutagen daemon runs as a subprocess with an isolated data directory.
//
// Usage:
//
//	daemon := mutagen.NewEmbeddedDaemon("~/.nexus/mutagen")
//	if err := daemon.Start(context.Background()); err != nil {
//	    log.Fatal(err)
//	}
//	defer daemon.Stop(context.Background())
//
//	sessionManager := mutagen.NewSessionManager(daemon)
//	session, err := sessionManager.CreateSession(...)
//
package mutagen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mutagen-io/mutagen/pkg/daemon"
	"github.com/mutagen-io/mutagen/pkg/grpcutil"
	"github.com/mutagen-io/mutagen/pkg/ipc"
	daemonsvc "github.com/mutagen-io/mutagen/pkg/service/daemon"
	"github.com/mutagen-io/mutagen/pkg/service/synchronization"
	"github.com/mutagen-io/mutagen/pkg/selection"
	syncpkg "github.com/mutagen-io/mutagen/pkg/synchronization"
	"github.com/mutagen-io/mutagen/pkg/url"
)

// EmbeddedDaemon manages an embedded Mutagen daemon instance.
//
// The daemon runs as a subprocess with an isolated data directory,
// preventing conflicts with any standalone Mutagen installation.
type EmbeddedDaemon struct {
	// dataDir is the Mutagen data directory (e.g., ~/.nexus/mutagen)
	dataDir string

	// socketPath is the path to the daemon's Unix socket
	socketPath string

	// cmd is the daemon subprocess
	cmd *exec.Cmd

	// conn is the gRPC connection to the daemon
	conn *grpc.ClientConn

	// mu protects started and conn
	mu sync.RWMutex

	// started indicates if the daemon has been started
	started bool

	// stopCh signals the monitor goroutine to stop
	stopCh chan struct{}

	// wg waits for the monitor goroutine
	wg sync.WaitGroup
}

// NewEmbeddedDaemon creates a new embedded daemon configuration.
// The daemon is not started until Start() is called.
//
// dataDir should be an absolute path where Mutagen will store its data.
// A good default is filepath.Join(homeDir, ".nexus", "mutagen").
func NewEmbeddedDaemon(dataDir string) *EmbeddedDaemon {
	// Expand ~ if present
	if len(dataDir) > 0 && dataDir[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, dataDir[1:])
		}
	}

	return &EmbeddedDaemon{
		dataDir:    dataDir,
		socketPath: filepath.Join(dataDir, "daemon", "daemon.sock"),
		stopCh:     make(chan struct{}),
	}
}

// Start launches the embedded Mutagen daemon.
//
// This method:
//  1. Creates the data directory if needed
//  2. Locates the mutagen binary (preferring bundled version)
//  3. Starts the daemon subprocess with MUTAGEN_DATA_DIRECTORY set
//  4. Waits for the daemon socket to be ready
//  5. Establishes a gRPC connection
//
// Start is safe to call multiple times - subsequent calls return nil if
// the daemon is already running.
func (d *EmbeddedDaemon) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.started {
		return nil
	}

	// Ensure data directory exists
	if err := os.MkdirAll(d.dataDir, 0700); err != nil {
		return fmt.Errorf("failed to create mutagen data directory: %w", err)
	}

	// Find the mutagen binary
	mutagenPath, err := d.findMutagenBinary()
	if err != nil {
		return fmt.Errorf("mutagen binary not found: %w", err)
	}

	// Set up environment with isolated data directory
	env := os.Environ()
	env = append(env, fmt.Sprintf("MUTAGEN_DATA_DIRECTORY=%s", d.dataDir))

	// Start daemon: mutagen daemon run
	d.cmd = &exec.Cmd{
		Path:   mutagenPath,
		Args:   []string{"mutagen", "daemon", "run"},
		Env:    env,
		Stdout: os.Stdout, // TODO: Redirect to structured logging
		Stderr: os.Stderr,
	}

	if err := d.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start mutagen daemon: %w", err)
	}

	// Wait for daemon to be ready
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := d.waitForReady(readyCtx); err != nil {
		d.cmd.Process.Kill()
		return fmt.Errorf("daemon failed to become ready: %w", err)
	}

	// Connect to daemon
	conn, err := d.connect()
	if err != nil {
		d.cmd.Process.Kill()
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}
	d.conn = conn

	d.started = true

	// Start monitoring goroutine
	d.wg.Add(1)
	go d.monitor()

	return nil
}

// findMutagenBinary locates the mutagen binary, preferring bundled version.
//
// Search order:
//  1. Same directory as the current executable
//  2. ../libexec/ relative to executable (FHS layout)
//  3. PATH lookup
func (d *EmbeddedDaemon) findMutagenBinary() (string, error) {
	// 1. Check for bundled binary next to nexus daemon
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)

		// Direct sibling
		bundled := filepath.Join(exeDir, "mutagen")
		if runtime.GOOS == "windows" {
			bundled += ".exe"
		}
		if info, err := os.Stat(bundled); err == nil && !info.IsDir() {
			return bundled, nil
		}

		// FHS libexec directory
		libexec := filepath.Join(exeDir, "..", "libexec", "mutagen")
		if runtime.GOOS == "windows" {
			libexec += ".exe"
		}
		if info, err := os.Stat(libexec); err == nil && !info.IsDir() {
			return libexec, nil
		}
	}

	// 2. Fallback to PATH
	return exec.LookPath("mutagen")
}

// waitForReady waits for the daemon socket to become available.
func (d *EmbeddedDaemon) waitForReady(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(d.socketPath); err == nil {
				// Socket exists, but give it a moment to be ready
				time.Sleep(50 * time.Millisecond)
				return nil
			}
		}
	}
}

// connect establishes a gRPC connection to the daemon.
func (d *EmbeddedDaemon) connect() (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return grpc.DialContext(
		ctx,
		"unix:"+d.socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(ipc.DialContext),
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(grpcutil.MaximumMessageSize),
			grpc.MaxCallRecvMsgSize(grpcutil.MaximumMessageSize),
		),
	)
}

// Connection returns the gRPC connection to the daemon.
// Returns nil if the daemon is not running.
func (d *EmbeddedDaemon) Connection() *grpc.ClientConn {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.conn
}

// IsRunning returns true if the daemon is running.
func (d *EmbeddedDaemon) IsRunning() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.started && d.conn != nil
}

// Stop gracefully shuts down the daemon.
//
// This method:
//  1. Signals the monitor goroutine to stop
//  2. Closes the gRPC connection
//  3. Requests daemon termination via API
//  4. Waits for the process to exit (with timeout)
//
// Stop is safe to call multiple times.
func (d *EmbeddedDaemon) Stop(ctx context.Context) error {
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return nil
	}
	d.started = false
	conn := d.conn
	d.conn = nil
	d.mu.Unlock()

	// Signal monitor to stop
	close(d.stopCh)

	// Close gRPC connection
	if conn != nil {
		conn.Close()
	}

	// Request daemon shutdown via API if possible
	if conn != nil {
		client := daemonsvc.NewDaemonClient(conn)
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// Ignore error - daemon might already be shutting down
		_, _ = client.Terminate(shutdownCtx, &daemonsvc.TerminateRequest{})
	}

	// Wait for process to exit
	if d.cmd != nil && d.cmd.Process != nil {
		done := make(chan error, 1)
		go func() {
			done <- d.cmd.Wait()
		}()

		select {
		case <-ctx.Done():
			d.cmd.Process.Kill()
			return ctx.Err()
		case <-done:
			// Process exited
		}
	}

	// Wait for monitor goroutine
	d.wg.Wait()

	return nil
}

// monitor watches the daemon process and handles unexpected exits.
func (d *EmbeddedDaemon) monitor() {
	defer d.wg.Done()

	if d.cmd == nil {
		return
	}

	processDone := make(chan error, 1)
	go func() {
		processDone <- d.cmd.Wait()
	}()

	select {
	case <-d.stopCh:
		// Intentional stop, nothing to do
		return
	case err := <-processDone:
		// Daemon exited unexpectedly
		d.mu.Lock()
		wasStarted := d.started
		d.mu.Unlock()

		if wasStarted {
			// TODO: Implement restart policy with backoff
			// For now, just log the error
			fmt.Fprintf(os.Stderr, "Mutagen daemon exited unexpectedly: %v\n", err)
		}
	}
}

// SessionManager provides high-level session management using an embedded daemon.
type SessionManager struct {
	daemon     *EmbeddedDaemon
	syncClient synchronization.SynchronizationClient
}

// NewSessionManager creates a session manager for the given daemon.
// The daemon must be started before using the session manager.
func NewSessionManager(daemon *EmbeddedDaemon) *SessionManager {
	return &SessionManager{
		daemon:     daemon,
		syncClient: synchronization.NewSynchronizationClient(daemon.Connection()),
	}
}

// SessionInfo holds information about a sync session.
type SessionInfo struct {
	ID   string
	Name string
}

// CreateSession creates a new synchronization session between host and container.
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Human-readable session name
//   - hostPath: Absolute path to host worktree
//   - containerID: Docker container ID
//   - containerPath: Path inside container (e.g., "/workspace")
//
// The session starts syncing immediately upon creation.
func (sm *SessionManager) CreateSession(
	ctx context.Context,
	name string,
	hostPath string,
	containerID string,
	containerPath string,
) (*SessionInfo, error) {
	// Parse alpha URL (host worktree)
	alpha, err := url.Parse(hostPath, url.Kind_Synchronization, true)
	if err != nil {
		return nil, fmt.Errorf("invalid host path: %w", err)
	}

	// Parse beta URL (Docker container)
	// Format: docker://<container_id><path>
	betaURL := fmt.Sprintf("docker://%s%s", containerID, containerPath)
	beta, err := url.Parse(betaURL, url.Kind_Synchronization, false)
	if err != nil {
		return nil, fmt.Errorf("invalid container path: %w", err)
	}

	// Create session specification
	spec := &synchronization.CreationSpecification{
		Alpha: alpha,
		Beta:  beta,
		Configuration: &syncpkg.Configuration{
			SynchronizationMode:  syncpkg.SynchronizationMode_TwoWaySafe,
			IgnoreVCS:            true,
			DefaultFileMode:      0644,
			DefaultDirectoryMode: 0755,
		},
		ConfigurationAlpha: &syncpkg.Configuration{
			WatchMode: syncpkg.WatchMode_WatchModePortable,
		},
		ConfigurationBeta: &syncpkg.Configuration{
			WatchMode: syncpkg.WatchMode_WatchModePortable,
		},
		Name: name,
		Labels: map[string]string{
			"nexus/workspace": name,
			"nexus/managed":   "true",
		},
	}

	// Create the session
	resp, err := sm.syncClient.Create(ctx, &synchronization.CreateRequest{
		Specification: spec,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create sync session: %w", err)
	}

	return &SessionInfo{
		ID:   resp.Session,
		Name: name,
	}, nil
}

// PauseSession pauses a synchronization session.
// This stops file synchronization but preserves the session state.
func (sm *SessionManager) PauseSession(ctx context.Context, sessionID string) error {
	_, err := sm.syncClient.Pause(ctx, &synchronization.PauseRequest{
		Selection: &selection.Selection{
			Specifications: []string{sessionID},
		},
	})
	return err
}

// ResumeSession resumes a paused synchronization session.
func (sm *SessionManager) ResumeSession(ctx context.Context, sessionID string) error {
	_, err := sm.syncClient.Resume(ctx, &synchronization.ResumeRequest{
		Selection: &selection.Selection{
			Specifications: []string{sessionID},
		},
	})
	return err
}

// TerminateSession permanently terminates a synchronization session.
// This removes the session and cleans up associated resources.
func (sm *SessionManager) TerminateSession(ctx context.Context, sessionID string) error {
	_, err := sm.syncClient.Terminate(ctx, &synchronization.TerminateRequest{
		Selection: &selection.Selection{
			Specifications: []string{sessionID},
		},
		SkipWaitForDestinations: false,
	})
	return err
}

// FlushSession forces a synchronization session to flush pending changes.
func (sm *SessionManager) FlushSession(ctx context.Context, sessionID string) error {
	_, err := sm.syncClient.Flush(ctx, &synchronization.FlushRequest{
		Selection: &selection.Selection{
			Specifications: []string{sessionID},
		},
	})
	return err
}

// SyncStatus represents the status of a sync session.
type SyncStatus struct {
	SessionID   string
	Name        string
	Status      string
	AlphaPath   string
	BetaPath    string
	LastError   string
	Conflicts   int
	FilesTotal  int64
	FilesSynced int64
}

// GetSessionStatus retrieves the current status of a session.
func (sm *SessionManager) GetSessionStatus(ctx context.Context, sessionID string) (*SyncStatus, error) {
	resp, err := sm.syncClient.List(ctx, &synchronization.ListRequest{
		Selection: &selection.Selection{
			Specifications: []string{sessionID},
		},
	})
	if err != nil {
		return nil, err
	}

	if len(resp.SessionStates) == 0 {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	state := resp.SessionStates[0]
	return &SyncStatus{
		SessionID:   state.Session.Identifier,
		Name:        state.Session.Name,
		Status:      state.Status.String(),
		AlphaPath:   state.Session.Alpha.Path,
		BetaPath:    state.Session.Beta.Path,
		LastError:   state.LastError,
		Conflicts:   len(state.Conflicts),
		FilesTotal:  state.Stats.TotalFileCount,
		FilesSynced: state.Stats.SyncedFileCount,
	}, nil
}

// ListSessions returns all sessions managed by Nexus.
func (sm *SessionManager) ListSessions(ctx context.Context) ([]*SyncStatus, error) {
	resp, err := sm.syncClient.List(ctx, &synchronization.ListRequest{
		Selection: &selection.Selection{
			LabelSelector: "nexus/managed == true",
		},
	})
	if err != nil {
		return nil, err
	}

	var sessions []*SyncStatus
	for _, state := range resp.SessionStates {
		sessions = append(sessions, &SyncStatus{
			SessionID:   state.Session.Identifier,
			Name:        state.Session.Name,
			Status:      state.Status.String(),
			AlphaPath:   state.Session.Alpha.Path,
			BetaPath:    state.Session.Beta.Path,
			LastError:   state.LastError,
			Conflicts:   len(state.Conflicts),
			FilesTotal:  state.Stats.TotalFileCount,
			FilesSynced: state.Stats.SyncedFileCount,
		})
	}

	return sessions, nil
}

//go:generate go run golang.org/x/tools/cmd/goimports@latest -w embedded_daemon.go
