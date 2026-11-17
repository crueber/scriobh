-- 1833 A trigger after a successful push: one signed POST to an address the owner chose (a CI service, a build
-- hook). The address's scheme, host and port are plain; its path and query, which may be the receiver's secret,
-- are sealed, as for webhooks. The signing secret is sealed, shown once and never returned. The body carries the
-- repository, the branch, the commit id and the project slug, and nothing of the manuscript.
CREATE TABLE git_triggers (
  remote_id       TEXT PRIMARY KEY REFERENCES git_remotes(id) ON DELETE CASCADE,
  project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  url             TEXT NOT NULL,            -- scheme, host and port only
  path_sealed     TEXT NOT NULL DEFAULT '',
  secret_sealed   TEXT NOT NULL,
  secret_hint     TEXT NOT NULL,
  enabled         INTEGER NOT NULL DEFAULT 1,
  disabled_reason TEXT NOT NULL DEFAULT '',
  last_status     TEXT NOT NULL DEFAULT '', -- a short line: "delivered, HTTP 204", "failed: timeout"; never a response body
  last_at         TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);

-- The outbox. A row is written when a push succeeds and is sent at least once; every attempt carries its id.
CREATE TABLE git_trigger_deliveries (
  id              TEXT PRIMARY KEY,
  remote_id       TEXT NOT NULL REFERENCES git_remotes(id) ON DELETE CASCADE,
  project_id      TEXT NOT NULL,
  push_id         TEXT NOT NULL,
  commit_sha      TEXT NOT NULL,
  branch          TEXT NOT NULL,
  state           TEXT NOT NULL CHECK (state IN ('pending','delivered','dead')),
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  last_status     TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  finished_at     TEXT
);

CREATE INDEX git_trigger_deliveries_due ON git_trigger_deliveries(state, next_attempt_at);
CREATE INDEX git_trigger_deliveries_remote ON git_trigger_deliveries(remote_id, created_at);
