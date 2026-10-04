package sprites

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TokenPath is $XDG_CONFIG_HOME/nexus/sprites/token (default ~/.config).
func TokenPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "nexus", "sprites", "token"), nil
}

// ResolveToken returns the API token: SPRITES_TOKEN, SPRITES_API_TOKEN, then
// the nexus-owned token file. It returns "" when none is set.
func ResolveToken() string {
	for _, k := range []string{"SPRITES_TOKEN", "SPRITES_API_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	p, err := TokenPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ParseToken accepts a raw token or a `[export ]SPRITES_TOKEN=...` line,
// optionally quoted.
func ParseToken(in string) (string, error) {
	for _, line := range strings.Split(in, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		for _, k := range []string{"SPRITES_TOKEN=", "SPRITES_API_TOKEN="} {
			line = strings.TrimPrefix(line, k)
		}
		line = strings.Trim(strings.TrimSpace(line), `"'`)
		if line == "" || strings.ContainsAny(line, " \t") {
			return "", errors.New("sprites: token input is not a single token")
		}
		return line, nil
	}
	return "", errors.New("sprites: empty token input")
}

// SaveToken writes the token atomically with mode 0600 (dir 0700).
func SaveToken(token string) error {
	p, err := TokenPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// RemoveToken deletes the token file; a missing file is not an error.
func RemoveToken() error {
	p, err := TokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sprites: remove token: %w", err)
	}
	return nil
}
