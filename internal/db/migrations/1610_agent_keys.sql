-- 1610 agent keys.

-- A key acts as the person who made it, narrowed by a scope and, optionally,
-- by a pin to one project or one compendium. Only the SHA-256 of the secret is
-- stored; the prefix is for display. The compendium pin carries no foreign key,
-- so this file does not depend on the compendium tables (the 1500 range).
CREATE TABLE api_keys (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  prefix        TEXT NOT NULL,
  secret_hash   TEXT NOT NULL UNIQUE,
  scope         TEXT NOT NULL CHECK (scope IN ('read','write','admin')),
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,
  compendium_id TEXT,
  is_agent      INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  last_used_at  TEXT,
  expires_at    TEXT,
  revoked_at    TEXT,
  CHECK (project_id IS NULL OR compendium_id IS NULL)
);

CREATE INDEX api_keys_user ON api_keys(user_id, revoked_at);
