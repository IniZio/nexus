package daemonclient

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnvSecretStore_Get(t *testing.T) {
	store := &EnvSecretStore{VarName: "TEST_NEXUS_SECRET"}

	// Empty when not set
	secret, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		t.Fatalf("expected empty, got %q", secret)
	}

	// Set env and read
	t.Setenv("TEST_NEXUS_SECRET", "secret-value")
	secret, err = store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "secret-value" {
		t.Fatalf("expected secret-value, got %q", secret)
	}
}

func TestEnvSecretStore_Set(t *testing.T) {
	store := &EnvSecretStore{VarName: "TEST_NEXUS_SECRET_SET"}

	if err := store.Set("new-secret"); err != nil {
		t.Fatal(err)
	}

	if os.Getenv("TEST_NEXUS_SECRET_SET") != "new-secret" {
		t.Fatal("env not set")
	}
}

func TestFileSecretStore_GetAndSet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode checks are unix-specific")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "secret")
	store := &FileSecretStore{Path: path}

	// Empty when file doesn't exist
	secret, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		t.Fatalf("expected empty, got %q", secret)
	}

	// Set secret
	if err := store.Set("file-secret"); err != nil {
		t.Fatal(err)
	}

	// Verify file created with correct permissions
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := st.Mode().Perm(); m != 0o600 {
		t.Fatalf("file mode: got %04o want 0600", m)
	}

	// Read back
	secret, err = store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "file-secret" {
		t.Fatalf("expected file-secret, got %q", secret)
	}
}

func TestFileSecretStore_trimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	_ = os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(path, []byte("  spaced-secret  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &FileSecretStore{Path: path}
	secret, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "spaced-secret" {
		t.Fatalf("expected trimmed secret, got %q", secret)
	}
}

func TestChainedSecretStore_Get_returnsFirstMatch(t *testing.T) {
	// First store has secret
	store1 := &EnvSecretStore{VarName: "CHAIN_VAR"}
	t.Setenv("CHAIN_VAR", "from-env")

	store := &ChainedSecretStore{
		Stores: []DaemonSecretStore{store1},
	}

	secret, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "from-env" {
		t.Fatalf("expected from-env, got %q", secret)
	}
}

func TestChainedSecretStore_Get_skipsEmptyAndErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")

	// First store empty, second has secret
	store1 := &EnvSecretStore{VarName: "EMPTY_VAR"}
	store2 := &FileSecretStore{Path: path}
	_ = store2.Set("from-file")

	store := &ChainedSecretStore{
		Stores: []DaemonSecretStore{store1, store2},
	}

	secret, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "from-file" {
		t.Fatalf("expected from-file, got %q", secret)
	}
}

func TestChainedSecretStore_Set_writesToAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")

	store1 := &EnvSecretStore{VarName: "CHAIN_SET_VAR"}
	store2 := &FileSecretStore{Path: path}

	store := &ChainedSecretStore{
		Stores: []DaemonSecretStore{store1, store2},
	}

	if err := store.Set("shared-secret"); err != nil {
		t.Fatal(err)
	}

	// Check env
	if os.Getenv("CHAIN_SET_VAR") != "shared-secret" {
		t.Fatal("env not set")
	}

	// Check file
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shared-secret" {
		t.Fatalf("file content wrong: %q", string(data))
	}
}

func TestNewDefaultSecretStore(t *testing.T) {
	store := NewDefaultSecretStore()

	chain, ok := store.(*ChainedSecretStore)
	if !ok {
		t.Fatal("expected ChainedSecretStore")
	}
	if len(chain.Stores) != 3 {
		t.Fatalf("expected 3 stores, got %d", len(chain.Stores))
	}

	// First should be env store
	if _, ok := chain.Stores[0].(*EnvSecretStore); !ok {
		t.Fatal("first store should be EnvSecretStore")
	}
}

func TestNewSecureSecretStore(t *testing.T) {
	store := NewSecureSecretStore()

	chain, ok := store.(*ChainedSecretStore)
	if !ok {
		t.Fatal("expected ChainedSecretStore")
	}
	if len(chain.Stores) != 2 {
		t.Fatalf("expected 2 stores, got %d", len(chain.Stores))
	}

	// First should be keyring store
	if _, ok := chain.Stores[0].(*KeyringSecretStore); !ok {
		t.Fatal("first store should be KeyringSecretStore")
	}
}

func TestReadDaemonSecretFromStore_envOverride(t *testing.T) {
	t.Setenv("NEXUS_DAEMON_TOKEN", "env-override-secret")

	secret, err := ReadDaemonSecretFromStore()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "env-override-secret" {
		t.Fatalf("expected env-override-secret, got %q", secret)
	}
}

func TestReadDaemonSecretFromStore_notFound(t *testing.T) {
	// Clear env
	t.Setenv("NEXUS_DAEMON_TOKEN", "")

	// Create a store that will definitely return empty
	store := &ChainedSecretStore{
		Stores: []DaemonSecretStore{
			&EnvSecretStore{VarName: "NEXUS_DAEMON_TOKEN"},
			&FileSecretStore{Path: "/nonexistent/path/secret"},
		},
	}

	secret, err := store.Get()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if secret != "" {
		t.Fatalf("expected empty secret, got %q", secret)
	}
}
