// Package testutil provides SQLite test helpers
package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteTestHelper provides utilities for SQLite testing
type SQLiteTestHelper struct {
	DB       *sql.DB
	DBPath   string
}

// NewSQLiteTestHelper creates a new SQLite test helper with temp database
func NewSQLiteTestHelper(t *testing.T) (*SQLiteTestHelper, error) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open test database: %w", err)
	}

	helper := &SQLiteTestHelper{
		DB:     db,
		DBPath: dbPath,
	}

	// Cleanup after test
	t.Cleanup(func() {
		helper.Cleanup()
	})

	return helper, nil
}

// Exec executes a SQL statement
func (h *SQLiteTestHelper) Exec(t *testing.T, sql string, args ...interface{}) {
	_, err := h.DB.Exec(sql, args...)
	if err != nil {
		t.Fatalf("Failed to execute SQL: %v", err)
	}
}

// QuerySingle queries a single value
func (h *SQLiteTestHelper) QuerySingle(t *testing.T, sql string, args ...interface{}) interface{} {
	var result interface{}
	err := h.DB.QueryRow(sql, args...).Scan(&result)
	if err != nil {
		t.Fatalf("Failed to query: %v", err)
	}
	return result
}

// RowExists checks if a row exists
func (h *SQLiteTestHelper) RowExists(t *testing.T, table string, where string, args ...interface{}) bool {
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, where)
	err := h.DB.QueryRow(query, args...).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check existence: %v", err)
	}
	return count > 0
}

// Count returns the count of rows in a table
func (h *SQLiteTestHelper) Count(t *testing.T, table string) int {
	var count int
	err := h.DB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count: %v", err)
	}
	return count
}

// Cleanup closes database and removes file
func (h *SQLiteTestHelper) Cleanup() {
	if h.DB != nil {
		_ = h.DB.Close()
	}
	if h.DBPath != "" {
		_ = os.Remove(h.DBPath)
	}
}
