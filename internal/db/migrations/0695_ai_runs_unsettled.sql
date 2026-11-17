-- 0695 a run whose settlement could not be written is marked 'unsettled'. It keeps
-- the amount reserved for it, and the budget counts that as spent for the month the
-- run began in, so the cost of a call that happened is never lost with the row that
-- was to record it. A CHECK list cannot be changed in place, so the table is rebuilt
-- with the longer list; the rows and the indexes are carried over unchanged.

CREATE TABLE ai_runs_new (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL,
  project_id      TEXT NOT NULL,
  document_id     TEXT NOT NULL DEFAULT '',
  recipe          TEXT NOT NULL,
  recipe_version  INTEGER NOT NULL DEFAULT 0,
  provider_id     TEXT NOT NULL DEFAULT '',
  model           TEXT NOT NULL DEFAULT '',
  via             TEXT NOT NULL DEFAULT 'session',
  status          TEXT NOT NULL CHECK (status IN ('running','ok','cancelled','error','blocked','unsettled')),
  error_code      TEXT NOT NULL DEFAULT '',
  input_tokens    INTEGER NOT NULL DEFAULT 0,
  output_tokens   INTEGER NOT NULL DEFAULT 0,
  usage_reported  INTEGER NOT NULL DEFAULT 0,
  cost_micros     INTEGER NOT NULL DEFAULT 0,
  cost_known      INTEGER NOT NULL DEFAULT 1,
  own_cost        INTEGER NOT NULL DEFAULT 0,
  reserved_micros INTEGER NOT NULL DEFAULT 0,
  reserved_tokens INTEGER NOT NULL DEFAULT 0,
  currency        TEXT NOT NULL DEFAULT 'USD',
  local           INTEGER NOT NULL DEFAULT 0,
  attempts        INTEGER NOT NULL DEFAULT 0,
  duration_ms     INTEGER NOT NULL DEFAULT 0,
  payload_hash    TEXT NOT NULL DEFAULT '',
  output_hash     TEXT NOT NULL DEFAULT '',
  scope_json      TEXT NOT NULL DEFAULT '{}',
  created_at      TEXT NOT NULL,
  finished_at     TEXT NOT NULL DEFAULT ''
);

INSERT INTO ai_runs_new SELECT id, user_id, project_id, document_id, recipe, recipe_version, provider_id, model, via, status, error_code, input_tokens, output_tokens,
  usage_reported, cost_micros, cost_known, own_cost, reserved_micros, reserved_tokens, currency, local, attempts, duration_ms, payload_hash, output_hash, scope_json,
  created_at, finished_at FROM ai_runs;

DROP TABLE ai_runs;
ALTER TABLE ai_runs_new RENAME TO ai_runs;

CREATE INDEX ai_runs_project ON ai_runs(project_id, created_at);
CREATE INDEX ai_runs_user ON ai_runs(user_id, created_at);
CREATE INDEX ai_runs_open ON ai_runs(status) WHERE status IN ('running', 'unsettled');
