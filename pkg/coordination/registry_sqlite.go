package coordination

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteRegistry struct {
	db           *sql.DB
	userRegistry UserRegistry
	mutex        sync.RWMutex
}

func NewSQLiteRegistry(dbPath string) (*SQLiteRegistry, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)

	registry := &SQLiteRegistry{
		db: db,
	}

	if err := registry.runMigrations(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	registry.userRegistry = &SQLiteUserRegistry{db: db}

	return registry, nil
}

func (r *SQLiteRegistry) runMigrations() error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS _schema_version (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema version table: %w", err)
	}

	var currentVersion int
	err = tx.QueryRow("SELECT COALESCE(MAX(version), 0) FROM _schema_version").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("failed to query schema version: %w", err)
	}

	for _, migration := range migrations {
		if migration.Version > currentVersion {
			if _, err := tx.Exec(migration.SQL); err != nil {
				return fmt.Errorf("failed to execute migration %d: %w", migration.Version, err)
			}

			if _, err := tx.Exec("INSERT INTO _schema_version (version) VALUES (?)", migration.Version); err != nil {
				return fmt.Errorf("failed to record migration %d: %w", migration.Version, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (r *SQLiteRegistry) Register(node *Node) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if node.ID == "" {
		return fmt.Errorf("node ID cannot be empty")
	}

	now := time.Now()
	node.CreatedAt = now
	node.UpdatedAt = now
	node.LastSeen = now

	return nil
}

func (r *SQLiteRegistry) Unregister(id string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return nil
}

func (r *SQLiteRegistry) Get(id string) (*Node, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return nil, fmt.Errorf("node not found: %s", id)
}

func (r *SQLiteRegistry) List() ([]*Node, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return []*Node{}, nil
}

func (r *SQLiteRegistry) Update(id string, updates map[string]interface{}) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return nil
}

func (r *SQLiteRegistry) SetStatus(id, status string) error {
	return r.Update(id, map[string]interface{}{"status": status})
}

func (r *SQLiteRegistry) GetByLabel(key, value string) ([]*Node, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return []*Node{}, nil
}

func (r *SQLiteRegistry) GetByCapability(capability string) ([]*Node, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return []*Node{}, nil
}

func (r *SQLiteRegistry) GetUserRegistry() UserRegistry {
	return r.userRegistry
}

func (r *SQLiteRegistry) StoreGitHubInstallation(installation *GitHubInstallation) error {
	if err := installation.Validate(); err != nil {
		return err
	}

	_, err := r.db.Exec(`
		INSERT INTO github_installations (
			installation_id, user_id, github_user_id, github_username, 
			repo_full_name, token, token_expires_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			token = excluded.token,
			token_expires_at = excluded.token_expires_at,
			github_username = excluded.github_username,
			updated_at = CURRENT_TIMESTAMP
	`, installation.InstallationID, installation.UserID, installation.GitHubUserID,
		installation.GitHubUsername, installation.RepoFullName, installation.Token,
		installation.TokenExpiresAt, time.Now(), time.Now())

	if err != nil {
		return fmt.Errorf("failed to store GitHub installation: %w", err)
	}

	return nil
}

func (r *SQLiteRegistry) GetGitHubInstallation(userID string) (*GitHubInstallation, error) {
	var installation GitHubInstallation

	err := r.db.QueryRow(`
		SELECT installation_id, user_id, github_user_id, github_username, 
		       repo_full_name, token, token_expires_at, created_at, updated_at
		FROM github_installations
		WHERE user_id = ?
	`, userID).Scan(
		&installation.InstallationID,
		&installation.UserID,
		&installation.GitHubUserID,
		&installation.GitHubUsername,
		&installation.RepoFullName,
		&installation.Token,
		&installation.TokenExpiresAt,
		&installation.CreatedAt,
		&installation.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("GitHub installation not found for user: %s", userID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get GitHub installation: %w", err)
	}

	return &installation, nil
}

func (r *SQLiteRegistry) DeleteGitHubInstallation(userID string) error {
	_, err := r.db.Exec("DELETE FROM github_installations WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("failed to delete GitHub installation: %w", err)
	}

	return nil
}

func (r *SQLiteRegistry) StoreGitHubFork(fork *GitHubFork) error {
	if err := fork.Validate(); err != nil {
		return err
	}

	_, err := r.db.Exec(`
		INSERT INTO github_forks (user_id, original_owner, original_repo, fork_owner, fork_url, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, original_owner, original_repo) DO NOTHING
	`, fork.UserID, fork.OriginalOwner, fork.OriginalRepo, fork.ForkOwner, fork.ForkURL, fork.CreatedAt)

	if err != nil {
		return fmt.Errorf("failed to store GitHub fork: %w", err)
	}

	return nil
}

func (r *SQLiteRegistry) GetGitHubFork(userID, originalOwner, originalRepo string) (*GitHubFork, error) {
	var fork GitHubFork

	err := r.db.QueryRow(`
		SELECT user_id, original_owner, original_repo, fork_owner, fork_url, created_at
		FROM github_forks
		WHERE user_id = ? AND original_owner = ? AND original_repo = ?
	`, userID, originalOwner, originalRepo).Scan(
		&fork.UserID,
		&fork.OriginalOwner,
		&fork.OriginalRepo,
		&fork.ForkOwner,
		&fork.ForkURL,
		&fork.CreatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("GitHub fork not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get GitHub fork: %w", err)
	}

	return &fork, nil
}

type SQLiteUserRegistry struct {
	db *sql.DB
	mu sync.RWMutex
}

func (r *SQLiteUserRegistry) Register(user *User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if user.Username == "" {
		return fmt.Errorf("username cannot be empty")
	}

	if user.ID == "" {
		user.ID = fmt.Sprintf("user_%d_%s", time.Now().Unix(), user.Username)
	}

	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now

	_, err := r.db.Exec(`
		INSERT INTO users (id, username, public_key, workspace_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, user.ID, user.Username, user.PublicKey, user.WorkspaceID, user.CreatedAt, user.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to register user: %w", err)
	}

	return nil
}

func (r *SQLiteUserRegistry) GetByUsername(username string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var user User

	err := r.db.QueryRow(`
		SELECT id, username, public_key, workspace_id, created_at, updated_at
		FROM users
		WHERE username = ?
	`, username).Scan(&user.ID, &user.Username, &user.PublicKey, &user.WorkspaceID, &user.CreatedAt, &user.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user not found: %s", username)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return &user, nil
}

func (r *SQLiteUserRegistry) GetByWorkspace(workspaceID string) ([]*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rows, err := r.db.Query(`
		SELECT id, username, public_key, workspace_id, created_at, updated_at
		FROM users
		WHERE workspace_id = ?
	`, workspaceID)

	if err != nil {
		return nil, fmt.Errorf("failed to get users: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Username, &user.PublicKey, &user.WorkspaceID, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, &user)
	}

	return users, nil
}

func (r *SQLiteUserRegistry) List() ([]*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rows, err := r.db.Query(`
		SELECT id, username, public_key, workspace_id, created_at, updated_at
		FROM users
	`)

	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Username, &user.PublicKey, &user.WorkspaceID, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, &user)
	}

	return users, nil
}

func (r *SQLiteUserRegistry) Delete(username string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec("DELETE FROM users WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	return nil
}
