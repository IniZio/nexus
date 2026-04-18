-- +goose Up
CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS spotlight_forwards (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  local_port INTEGER NOT NULL,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_spotlight_forwards_local_port ON spotlight_forwards(local_port);

-- +goose Down
DROP INDEX IF EXISTS idx_spotlight_forwards_local_port;
DROP TABLE IF EXISTS spotlight_forwards;
DROP TABLE IF EXISTS workspaces;
