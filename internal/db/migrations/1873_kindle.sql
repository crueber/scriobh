-- 1873 Send to Kindle: a person's own address, the one-time previews, and the per-project send log.
CREATE TABLE kindle_addresses (
  user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  address    TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- A preview is bound to one person, project, address and edition. The token is shown once; only its hash is kept.
CREATE TABLE kindle_previews (
  token_hash TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  address    TEXT NOT NULL,
  input_hash TEXT NOT NULL,
  file_name  TEXT NOT NULL,
  file_bytes INTEGER NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX kindle_previews_user ON kindle_previews(user_id);

-- Who sent which edition of which project when, and to which domain. No address, no text.
CREATE TABLE kindle_sends (
  id               TEXT PRIMARY KEY,
  project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  outbox_id        TEXT NOT NULL,
  input_hash       TEXT NOT NULL,
  file_sha256      TEXT NOT NULL,
  file_name        TEXT NOT NULL,
  file_bytes       INTEGER NOT NULL,
  recipient_domain TEXT NOT NULL,
  created_at       TEXT NOT NULL
);
CREATE INDEX kindle_sends_project ON kindle_sends(project_id, created_at);
