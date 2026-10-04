package broker

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	caCertFile       = "ca.pem"
	caKeyFile        = "ca.key"
	placeholdersFile = "placeholders.json"
	// placeholderBytes mirrors the entropy cred mints per placeholder
	// (hex-encoded, so the placeholder is twice as long).
	placeholderBytes = 32
)

// writeFileAtomic writes data to dir/name with mode 0600 via temp+rename.
func writeFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// loadCA returns the persisted CA pair, or nil slices when absent.
func loadCA(dir string) (cert, key []byte) {
	cert, err1 := os.ReadFile(filepath.Join(dir, caCertFile))
	key, err2 := os.ReadFile(filepath.Join(dir, caKeyFile))
	if err1 != nil || err2 != nil || len(cert) == 0 || len(key) == 0 {
		return nil, nil
	}
	return cert, key
}

func saveCA(dir string, cert, key []byte) error {
	if err := writeFileAtomic(dir, caKeyFile, key); err != nil {
		return err
	}
	return writeFileAtomic(dir, caCertFile, cert)
}

// loadPlaceholders returns name->placeholder; unreadable or malformed
// content yields an empty map so the caller mints fresh values.
func loadPlaceholders(dir string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, placeholdersFile))
	if err != nil {
		return out
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return out
	}
	for k, v := range m {
		if b, err := hex.DecodeString(v); err == nil && len(b) == placeholderBytes && v == strings.ToLower(v) {
			out[k] = v
		}
	}
	return out
}

func savePlaceholders(dir string, m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(dir, placeholdersFile, data)
}
