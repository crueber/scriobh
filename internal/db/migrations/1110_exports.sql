-- 1110 exports: every export is recorded with a manifest of what it contained.

CREATE TABLE exports (
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id       TEXT NOT NULL REFERENCES users(id),
  preset        TEXT NOT NULL DEFAULT '',
  format        TEXT NOT NULL CHECK (format IN ('md','html','docx','epub','pdf')),
  filename      TEXT NOT NULL,
  bytes         INTEGER NOT NULL DEFAULT 0,
  sha256        TEXT NOT NULL DEFAULT '',
  manifest_json TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL
);

CREATE INDEX exports_project ON exports(project_id, created_at);
