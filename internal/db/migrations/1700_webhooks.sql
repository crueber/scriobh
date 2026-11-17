-- 1700 webhooks.

-- A webhook belongs to a project and is managed by its owner. The signing secret
-- is sealed with the instance key (internal/secrets), never stored in the clear,
-- and shown once, when it is made or rotated. Deleting the project deletes its
-- webhooks and, through them, their deliveries.
CREATE TABLE webhooks (
  id                 TEXT PRIMARY KEY,
  project_id         TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name               TEXT NOT NULL,
  url                TEXT NOT NULL,            -- scheme, host and port only
  url_path_sealed    TEXT NOT NULL DEFAULT '', -- the path and query, sealed: they may be a receiver's secret
  events_json        TEXT NOT NULL,
  include_text       INTEGER NOT NULL DEFAULT 0,
  enabled            INTEGER NOT NULL DEFAULT 1,
  disabled_reason    TEXT NOT NULL DEFAULT '',
  secret_sealed      TEXT NOT NULL,
  secret_hint        TEXT NOT NULL,
  prev_secret_sealed TEXT NOT NULL DEFAULT '',
  prev_secret_until  TEXT NOT NULL DEFAULT '',
  failure_streak     INTEGER NOT NULL DEFAULT 0,
  created_by         TEXT,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  last_delivery_at   TEXT,
  last_status        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX webhooks_project ON webhooks(project_id);

-- The outbox. A row is written in the same transaction that moves the feeder's
-- cursor past the event it came from, so an event becomes exactly one delivery
-- per webhook and a crash loses nothing. payload_json is the body without any
-- text a person wrote (ids and counts); a comment's words, when the owner asked
-- for them, are added at send time and never stored here. A delivery is sent at
-- least once: the same id is sent on every attempt.
CREATE TABLE webhook_deliveries (
  id              TEXT PRIMARY KEY,
  webhook_id      TEXT NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  project_id      TEXT NOT NULL,
  event           TEXT NOT NULL,
  event_seq       INTEGER,
  doc_id          TEXT NOT NULL DEFAULT '',
  payload_json    TEXT NOT NULL,
  state           TEXT NOT NULL CHECK (state IN ('pending','delivered','dead')),
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  claimed_at      TEXT,
  last_status     INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT NOT NULL DEFAULT '',
  last_excerpt    TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  delivered_at    TEXT,
  UNIQUE (webhook_id, event_seq)
);

CREATE INDEX webhook_deliveries_due ON webhook_deliveries(state, next_attempt_at);
CREATE INDEX webhook_deliveries_hook_state ON webhook_deliveries(webhook_id, state, next_attempt_at);
CREATE INDEX webhook_deliveries_project ON webhook_deliveries(project_id, state);
CREATE INDEX webhook_deliveries_hook ON webhook_deliveries(webhook_id, created_at);

-- The feeder's place in the events table. Seeded at the newest event, so a
-- webhook never hears of what happened before there was one.
CREATE TABLE webhook_state (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  cursor  INTEGER NOT NULL,
  dropped INTEGER NOT NULL DEFAULT 0
);

INSERT INTO webhook_state(id, cursor) VALUES (1, (SELECT COALESCE(max(seq), 0) FROM events));
