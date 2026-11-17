-- 0600 AI providers and prices. A provider is an endpoint the administrator (or,
-- for a provider of their own, a person) has configured; nothing is ever sent to
-- an address that is not in this table. The key is sealed (internal/secrets)
-- under a context that binds the row, its owner and its normalised address, so
-- the ciphertext does not open if it is copied to another row or the address is
-- changed in the database. No op returns it; key_hint holds the last four
-- characters for the "replace" screen and nothing more.

CREATE TABLE ai_providers (
  id                TEXT PRIMARY KEY,
  user_id           TEXT REFERENCES users(id) ON DELETE CASCADE, -- NULL: an instance provider an administrator manages
  kind              TEXT NOT NULL CHECK (kind IN ('openai_compatible','openrouter','opencode','ollama')),
  name              TEXT NOT NULL,
  base_url          TEXT NOT NULL,
  api_key_cipher    TEXT NOT NULL DEFAULT '',
  key_hint          TEXT NOT NULL DEFAULT '',
  default_model     TEXT NOT NULL DEFAULT '',
  models_json       TEXT NOT NULL DEFAULT '[]',   -- cached list from the provider, plus manual entries
  models_fetched_at TEXT NOT NULL DEFAULT '',
  settings_json     TEXT NOT NULL DEFAULT '{}',   -- extra headers (allow-list), routing preferences, username
  enabled           INTEGER NOT NULL DEFAULT 1,
  local             INTEGER NOT NULL DEFAULT 0,   -- declared by an administrator: loopback and private addresses are allowed
  timeout_seconds   INTEGER NOT NULL DEFAULT 0,   -- 0: the server's default
  max_concurrency   INTEGER NOT NULL DEFAULT 0,   -- 0: the server's default
  created_by        TEXT,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);

CREATE UNIQUE INDEX ai_providers_name ON ai_providers(COALESCE(user_id, ''), lower(name));
CREATE INDEX ai_providers_user ON ai_providers(user_id);

-- Prices an administrator sets, in micro-units of the instance currency per
-- million tokens. provider_id '' means any provider. A model with no price here
-- and none listed by its provider has an unknown price, which is never treated
-- as zero.
CREATE TABLE ai_prices (
  provider_id   TEXT NOT NULL DEFAULT '',
  model         TEXT NOT NULL,
  input_micros  INTEGER NOT NULL CHECK (input_micros >= 0),
  output_micros INTEGER NOT NULL CHECK (output_micros >= 0),
  updated_at    TEXT NOT NULL,
  PRIMARY KEY (provider_id, model)
) WITHOUT ROWID;
