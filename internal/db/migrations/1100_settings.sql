-- 1100 admin settings. Instance configuration editable in the UI.
-- Secrets never live here; they stay in the environment or the key file.

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
