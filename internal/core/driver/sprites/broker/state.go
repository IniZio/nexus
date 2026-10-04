// Package broker manages the host-side credential broker process that serves
// one sprites sandbox.
package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

const (
	stateFile = "broker.json"
	// backendDir mirrors the sprites driver's per-backend state subdirectory.
	backendDir = "sprites"
)

// State is the on-disk record of a running broker.
type State struct {
	PID     int       `json:"pid"`
	Exe     string    `json:"exe"`
	Cmdline []string  `json:"cmdline"`
	Started time.Time `json:"started"`
	// CAFingerprint is the hex SHA-256 of the broker CA certificate.
	CAFingerprint string `json:"ca_fingerprint"`
	// Listen describes where the broker accepts connections.
	Listen string `json:"listen"`
	// GuestEnv is the env the guest exec gets: placeholders plus proxy and CA vars.
	GuestEnv map[string]string `json:"guest_env"`
	// GuestCAPath is the CA bundle path inside the guest.
	GuestCAPath string `json:"guest_ca_path"`
}

// Dir returns the per-sandbox state directory under stateDir. The id is
// validated and must be in canonical form so it cannot escape stateDir.
func Dir(stateDir, id string) (string, error) {
	parsed, err := domain.ParseSandboxID(id)
	if err != nil {
		return "", fmt.Errorf("broker: invalid sandbox id: %w", err)
	}
	if parsed.String() != id || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("broker: non-canonical sandbox id %q", id)
	}
	return filepath.Join(stateDir, backendDir, id), nil
}

// StatePath returns the broker.json path for the sandbox.
func StatePath(stateDir, id string) (string, error) {
	d, err := Dir(stateDir, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, stateFile), nil
}

// WriteState atomically writes broker.json with mode 0600.
func WriteState(stateDir, id string, st State) error {
	p, err := StatePath(stateDir, id)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("broker: mkdir state dir: %w", err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, stateFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("broker: create temp state: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		cleanup()
		return fmt.Errorf("broker: rename state: %w", err)
	}
	return nil
}

// ReadState loads broker.json. A missing file yields an error matching
// fs.ErrNotExist.
func ReadState(stateDir, id string) (State, error) {
	p, err := StatePath(stateDir, id)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("broker: parse %s: %w", p, err)
	}
	return st, nil
}

// RemoveState deletes broker.json. A missing file is not an error.
func RemoveState(stateDir, id string) error {
	p, err := StatePath(stateDir, id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
