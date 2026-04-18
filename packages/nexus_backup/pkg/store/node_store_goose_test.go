package store_test

import (
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

func TestNodeStore_CreatesGooseVersionTable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	has, err := st.HasTable("goose_db_version")
	if err != nil {
		t.Fatalf("check goose table: %v", err)
	}
	if !has {
		t.Fatal("expected goose_db_version table to exist")
	}
}
