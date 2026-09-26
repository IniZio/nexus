package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type storedEntry struct {
	Key    Key    `json:"key"`
	Record Record `json:"record"`
}

// FileStore implements vault.Store as one encrypted file per record.
// Files live under dir with mode 0600; writes are atomic (temp + rename).
type FileStore struct {
	dir string
	key []byte
}

// NewFileStore creates a FileStore rooted at dir (created 0700 if absent).
func NewFileStore(dir string, encKey []byte) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("vault: mkdir store dir: %w", err)
	}
	return &FileStore{dir: dir, key: encKey}, nil
}

func (s *FileStore) baseName(k Key) string {
	h := sha256.Sum256([]byte(k.Principal + "\x00" + k.Integration))
	return hex.EncodeToString(h[:])
}

func (s *FileStore) filePath(k Key) string {
	return filepath.Join(s.dir, s.baseName(k))
}

func (s *FileStore) metaPath(k Key) string {
	return s.filePath(k) + ".meta"
}

func (s *FileStore) Get(_ context.Context, k Key) (Record, error) {
	data, err := os.ReadFile(s.filePath(k))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, ErrUnlinked
		}
		return Record{}, fmt.Errorf("vault: read record: %w", err)
	}
	pt, err := decryptRecord(data, s.key, k)
	if err != nil {
		return Record{}, err
	}
	var entry storedEntry
	if err := json.Unmarshal(pt, &entry); err != nil {
		return Record{}, fmt.Errorf("vault: unmarshal record: %w", err)
	}
	return entry.Record, nil
}

func (s *FileStore) Put(_ context.Context, k Key, rec Record) error {
	pt, err := json.Marshal(storedEntry{Key: k, Record: rec})
	if err != nil {
		return fmt.Errorf("vault: marshal record: %w", err)
	}
	ct, err := encryptRecord(pt, s.key, k)
	if err != nil {
		return err
	}
	dest := s.filePath(k)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, ct, 0600); err != nil {
		return fmt.Errorf("vault: write temp file: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("vault: atomic rename: %w", err)
	}
	metaBytes, err := json.Marshal(k)
	if err != nil {
		return fmt.Errorf("vault: marshal meta: %w", err)
	}
	return os.WriteFile(s.metaPath(k), metaBytes, 0600)
}

func (s *FileStore) Delete(_ context.Context, k Key) error {
	err := os.Remove(s.filePath(k))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(s.metaPath(k))
	return nil
}

func (s *FileStore) List(_ context.Context) ([]Key, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("vault: list store dir: %w", err)
	}
	var keys []Key
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".meta") {
			continue
		}
		metaBytes, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		var k Key
		if err := json.Unmarshal(metaBytes, &k); err != nil {
			continue
		}
		keys = append(keys, k)
	}
	return keys, nil
}
