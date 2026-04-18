-- +goose Up
CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL DEFAULT '',
  data TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workspaces_project_id ON workspaces(project_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_state ON workspaces(state);

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  data TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS spotlight_forwards (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  data TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_spotlight_forwards_workspace_id ON spotlight_forwards(workspace_id);

CREATE TABLE IF NOT EXISTS sandbox_resource_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  data TEXT NOT NULL
);

-- +goose Down
DROP INDEX IF EXISTS idx_spotlight_forwards_workspace_id;
DROP INDEX IF EXISTS idx_workspaces_state;
DROP INDEX IF EXISTS idx_workspaces_project_id;
DROP TABLE IF EXISTS spotlight_forwards;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS sandbox_resource_settings;
