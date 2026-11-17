-- 1600 idempotency.

-- A retried write with the same key returns the stored response instead of
-- applying twice. principal_id is "key:<id>" for an API key and "user:<id>"
-- for a browser session. status_code 0 is a tombstone: the request ran but its
-- response held a secret (or was too large) and was not kept.
CREATE TABLE idempotency_keys (
  principal_id  TEXT NOT NULL,
  key           TEXT NOT NULL,
  op            TEXT NOT NULL,
  request_hash  TEXT NOT NULL,
  response_json TEXT NOT NULL DEFAULT '',
  status_code   INTEGER NOT NULL,
  created_at    TEXT NOT NULL,
  expires_at    TEXT NOT NULL,
  PRIMARY KEY (principal_id, key)
) WITHOUT ROWID;

CREATE INDEX idempotency_keys_expiry ON idempotency_keys(expires_at);
