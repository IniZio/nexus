package coordination

const (
	DBVersion = 1
)

type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migrations = []Migration{
	{
		Version: 1,
		Name:    "initial_schema",
		SQL: `
CREATE TABLE IF NOT EXISTS github_installations (
	id INTEGER PRIMARY KEY,
	installation_id INTEGER,
	user_id TEXT NOT NULL UNIQUE,
	github_user_id INTEGER NOT NULL,
	github_username TEXT NOT NULL,
	repo_full_name TEXT,
	token TEXT NOT NULL,
	token_expires_at DATETIME NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS github_forks (
	id INTEGER PRIMARY KEY,
	user_id TEXT NOT NULL,
	original_owner TEXT NOT NULL,
	original_repo TEXT NOT NULL,
	fork_owner TEXT NOT NULL,
	fork_url TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(user_id, original_owner, original_repo),
	FOREIGN KEY(user_id) REFERENCES github_installations(user_id)
);

CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	public_key TEXT,
	workspace_id TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS workspaces (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	workspace_name TEXT NOT NULL,
	status TEXT,
	provider TEXT,
	image TEXT,
	repo_owner TEXT,
	repo_name TEXT,
	repo_url TEXT,
	repo_branch TEXT,
	repo_commit TEXT,
	fork_created BOOLEAN DEFAULT 0,
	fork_url TEXT,
	ssh_port INTEGER,
	ssh_host TEXT,
	node_id TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY(user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS services (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	service_name TEXT NOT NULL,
	command TEXT NOT NULL,
	port INTEGER NOT NULL,
	local_port INTEGER,
	status TEXT,
	health_status TEXT,
	last_health_check DATETIME,
	depends_on TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY(workspace_id) REFERENCES workspaces(id)
);

CREATE INDEX IF NOT EXISTS idx_github_installations_user_id ON github_installations(user_id);
CREATE INDEX IF NOT EXISTS idx_github_forks_user_id ON github_forks(user_id);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_workspaces_user_id ON workspaces(user_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_status ON workspaces(status);
CREATE INDEX IF NOT EXISTS idx_services_workspace_id ON services(workspace_id);
`,
	},
}
