package mutagen

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type EmbeddedDaemon struct {
	dataDir    string
	mutagenBin string
	socketPath string
	cmd        *exec.Cmd
	mu         sync.RWMutex
	running    bool
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewEmbeddedDaemon(dataDir string) *EmbeddedDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &EmbeddedDaemon{
		dataDir:    dataDir,
		mutagenBin: filepath.Join(dataDir, "bin", "mutagen"),
		socketPath: filepath.Join(dataDir, "daemon", "daemon.sock"),
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (d *EmbeddedDaemon) DataDir() string {
	return d.dataDir
}

func (d *EmbeddedDaemon) SocketPath() string {
	return d.socketPath
}

func (d *EmbeddedDaemon) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return nil
	}

	if err := os.MkdirAll(filepath.Join(d.dataDir, "daemon"), 0755); err != nil {
		return fmt.Errorf("failed to create daemon directory: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(d.dataDir, "bin"), 0755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	if err := d.ensureMutagenBinary(); err != nil {
		return fmt.Errorf("failed to ensure mutagen binary: %w", err)
	}

	log.Printf("[mutagen] Starting embedded daemon with data dir: %s", d.dataDir)

	d.cmd = exec.CommandContext(d.ctx, d.mutagenBin, "daemon", "run")
	d.cmd.Env = append(os.Environ(), "MUTAGEN_DATA_DIRECTORY="+d.dataDir)
	d.cmd.Stdout = os.Stdout
	d.cmd.Stderr = os.Stderr

	if err := d.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start mutagen daemon: %w", err)
	}

	if err := d.waitForSocket(30 * time.Second); err != nil {
		d.cmd.Process.Kill()
		return fmt.Errorf("daemon socket not ready: %w", err)
	}

	d.running = true
	log.Printf("[mutagen] Embedded daemon started successfully")

	return nil
}

func (d *EmbeddedDaemon) ensureMutagenBinary() error {
	binPath, err := ExtractMutagen(d.dataDir)
	if err == nil {
		d.mutagenBin = binPath
		return nil
	}

	log.Printf("[mutagen] Embedded extraction failed: %v, trying system mutagen", err)

	if path, err := exec.LookPath("mutagen"); err == nil {
		d.mutagenBin = path
		log.Printf("[mutagen] Using system mutagen at: %s", path)
		return nil
	}

	return fmt.Errorf("mutagen not available (embedded or system)")
}

func (d *EmbeddedDaemon) waitForSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	
	for time.Now().Before(deadline) {
		if _, err := os.Stat(d.socketPath); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	
	return fmt.Errorf("socket not found after timeout: %s", d.socketPath)
}

func (d *EmbeddedDaemon) Stop(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return nil
	}

	d.cancel()

	if d.cmd != nil && d.cmd.Process != nil {
		log.Printf("[mutagen] Stopping embedded daemon (PID: %d)", d.cmd.Process.Pid)
		
		if err := d.cmd.Process.Kill(); err != nil {
			log.Printf("[mutagen] Warning: failed to kill daemon process: %v", err)
		}
		
		done := make(chan error, 1)
		go func() {
			done <- d.cmd.Wait()
		}()
		
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Printf("[mutagen] Warning: daemon wait timed out")
		}
	}

	d.running = false
	log.Printf("[mutagen] Embedded daemon stopped")
	
	return nil
}

func (d *EmbeddedDaemon) IsRunning() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	
	if !d.running {
		return false
	}
	
	_, err := os.Stat(d.socketPath)
	return err == nil
}

func (d *EmbeddedDaemon) Running() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.running
}
