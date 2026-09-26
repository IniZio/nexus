package vault_test

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func newTestFileStore(t *testing.T) *vault.FileStore {
	t.Helper()
	dir := t.TempDir()
	encKey := make([]byte, 32)
	if _, err := rand.Read(encKey); err != nil {
		t.Fatal(err)
	}
	store, err := vault.NewFileStore(dir, encKey)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFileStorePassesStoreContract(t *testing.T) {
	vaulttest.RunStoreContract(t, newTestFileStore(t))
}

func TestXChaChaAADBindsPrincipalAndIntegration(t *testing.T) {
	encKey := make([]byte, 32)
	rand.Read(encKey)

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	store1, _ := vault.NewFileStore(dir1, encKey)
	store2, _ := vault.NewFileStore(dir2, encKey)

	ctx := context.Background()
	k1 := vault.Key{Principal: "local:alice", Integration: "github"}
	k2 := vault.Key{Principal: "local:alice", Integration: "linear"}
	rec := vault.Record{AccessToken: "secret", Expiry: time.Now().Add(time.Hour), AllowedProjects: []string{"*"}}

	if err := store1.Put(ctx, k1, rec); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir1)
	var encFile string
	for _, e := range entries {
		n := e.Name()
		if len(n) == 64 {
			encFile = filepath.Join(dir1, n)
		}
	}
	if encFile == "" {
		t.Fatal("encrypted file not found")
	}

	data, _ := os.ReadFile(encFile)
	destPath := filepath.Join(dir2, filepath.Base(encFile))
	_ = os.WriteFile(destPath, data, 0600)

	_, err := store2.Get(ctx, k2)
	if err == nil {
		t.Fatal("AAD swap should have failed decryption")
	}
}

func TestFileStoreAtomicRename0600(t *testing.T) {
	dir := t.TempDir()
	encKey := make([]byte, 32)
	rand.Read(encKey)
	store, _ := vault.NewFileStore(dir, encKey)

	ctx := context.Background()
	k := vault.Key{Principal: "local:bob", Integration: "github"}
	rec := vault.Record{AccessToken: "tok", Expiry: time.Now().Add(time.Hour), AllowedProjects: []string{"*"}}

	if err := store.Put(ctx, k, rec); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "." || e.Name() == ".." {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		perm := info.Mode().Perm()
		if perm != 0600 {
			t.Errorf("file %s has mode %v, want 0600", e.Name(), perm)
		}
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}
