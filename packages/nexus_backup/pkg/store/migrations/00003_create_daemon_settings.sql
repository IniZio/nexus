-- +goose Up
CREATE TABLE IF NOT EXISTS sandbox_resource_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  default_memory_mib INTEGER NOT NULL,
  default_vcpus INTEGER NOT NULL,
  max_memory_mib INTEGER NOT NULL,
  max_vcpus INTEGER NOT NULL,
  updated_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS sandbox_resource_settings;
