-- 1000 events and audit.

-- seq is commit order. Payloads carry ids and counts, never prose.
CREATE TABLE events (
  seq          INTEGER PRIMARY KEY AUTOINCREMENT,
  type         TEXT NOT NULL,
  project_id   TEXT,
  document_id  TEXT,
  actor_id     TEXT,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL
);

CREATE INDEX events_project ON events(project_id, seq);

CREATE TABLE audit_log (
  id          TEXT PRIMARY KEY,
  at          TEXT NOT NULL,
  actor_id    TEXT,
  action      TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT '',
  target_id   TEXT NOT NULL DEFAULT '',
  ip_hash     TEXT NOT NULL DEFAULT '',
  detail_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX audit_log_at ON audit_log(at);
CREATE INDEX audit_log_actor ON audit_log(actor_id, at);
