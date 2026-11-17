-- 0100 auth: identity and sessions.

CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  oidc_issuer   TEXT,
  oidc_sub      TEXT,
  email         TEXT,
  display_name  TEXT NOT NULL DEFAULT '',
  avatar_color  TEXT NOT NULL DEFAULT '#2F5D50',
  timezone      TEXT NOT NULL DEFAULT 'UTC',
  locale        TEXT NOT NULL DEFAULT 'en',
  role          TEXT NOT NULL DEFAULT 'reader' CHECK (role IN ('admin','writer','reader')),
  is_agent      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  last_seen_at  TEXT,
  deleted_at    TEXT
);

-- Identity is issuer|sub, bound once.
-- Partial on deleted_at so a removed account does not block a new one; sign-in
-- looks removed accounts up explicitly and refuses them.
CREATE UNIQUE INDEX users_identity ON users(oidc_issuer, oidc_sub) WHERE oidc_sub IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX users_email ON users(email COLLATE NOCASE) WHERE email IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE sessions (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash      TEXT NOT NULL UNIQUE,
  created_at      TEXT NOT NULL,
  last_seen_at    TEXT NOT NULL,
  expires_at      TEXT NOT NULL,
  idle_expires_at TEXT,
  user_agent      TEXT NOT NULL DEFAULT '',
  ip_hash         TEXT NOT NULL DEFAULT '',
  revoked_at      TEXT
);

CREATE INDEX sessions_user ON sessions(user_id, revoked_at);
