-- +goose Up
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_created_at ON projects(created_at);

-- Add project_id to workspaces (nullable initially for migration)
ALTER TABLE workspaces ADD COLUMN project_id TEXT;

CREATE INDEX IF NOT EXISTS idx_workspaces_project_id ON workspaces(project_id);

-- +goose Down
DROP INDEX IF EXISTS idx_workspaces_project_id;
DROP INDEX IF EXISTS idx_projects_created_at;
ALTER TABLE workspaces DROP COLUMN project_id;
DROP TABLE IF EXISTS projects;
