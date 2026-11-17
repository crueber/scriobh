-- 0620 AI runs, daily usage and budgets.
--
-- A run row records that a request happened: who, where, which recipe, provider
-- and model, how many tokens, what it cost, how long it took, and a hash of the
-- payload that was sent. It holds NO prompt and NO completion text. The rows
-- are the audit trail and the reservation ledger: a running row carries the
-- worst-case cost reserved for it, so concurrent runs cannot all pass a cap that
-- only one of them fits under.
--
-- These tables have no foreign keys on purpose. Deleting a project or a person
-- must not reset a monthly spend, and the usage record outlives what it
-- describes.

CREATE TABLE ai_runs (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL,
  project_id      TEXT NOT NULL,
  document_id     TEXT NOT NULL DEFAULT '',
  recipe          TEXT NOT NULL,
  recipe_version  INTEGER NOT NULL DEFAULT 0,
  provider_id     TEXT NOT NULL DEFAULT '',
  model           TEXT NOT NULL DEFAULT '',
  via             TEXT NOT NULL DEFAULT 'session',   -- session or apikey
  status          TEXT NOT NULL CHECK (status IN ('running','ok','cancelled','error','blocked')),
  error_code      TEXT NOT NULL DEFAULT '',
  input_tokens    INTEGER NOT NULL DEFAULT 0,
  output_tokens   INTEGER NOT NULL DEFAULT 0,
  usage_reported  INTEGER NOT NULL DEFAULT 0,        -- 1: the provider's counts; 0: an estimate
  cost_micros     INTEGER NOT NULL DEFAULT 0,        -- what the run cost, charged against the currency caps
  cost_known      INTEGER NOT NULL DEFAULT 1,        -- 0: no price was known
  own_cost        INTEGER NOT NULL DEFAULT 0,        -- 1: the person's own provider paid; not charged against the instance caps
  reserved_micros INTEGER NOT NULL DEFAULT 0,        -- worst case held while running
  reserved_tokens INTEGER NOT NULL DEFAULT 0,
  currency        TEXT NOT NULL DEFAULT 'USD',
  local           INTEGER NOT NULL DEFAULT 0,        -- the connection went to a local address
  attempts        INTEGER NOT NULL DEFAULT 0,        -- providers tried
  duration_ms     INTEGER NOT NULL DEFAULT 0,
  payload_hash    TEXT NOT NULL DEFAULT '',
  output_hash     TEXT NOT NULL DEFAULT '',
  scope_json      TEXT NOT NULL DEFAULT '{}',        -- chip kinds, labels and token counts: never prose
  created_at      TEXT NOT NULL,
  finished_at     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX ai_runs_project ON ai_runs(project_id, created_at);
CREATE INDEX ai_runs_user ON ai_runs(user_id, created_at);
CREATE INDEX ai_runs_running ON ai_runs(status) WHERE status = 'running';

CREATE TABLE ai_usage_daily (
  user_id       TEXT NOT NULL,
  project_id    TEXT NOT NULL,
  day           TEXT NOT NULL,                       -- YYYY-MM-DD, UTC
  runs          INTEGER NOT NULL DEFAULT 0,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_micros   INTEGER NOT NULL DEFAULT 0,          -- charged against the currency caps
  own_micros    INTEGER NOT NULL DEFAULT 0,          -- paid by the person's own provider
  PRIMARY KEY (user_id, project_id, day)
) WITHOUT ROWID;

CREATE INDEX ai_usage_day ON ai_usage_daily(day);
CREATE INDEX ai_usage_project ON ai_usage_daily(project_id, day);

-- Monthly caps. scope_id is '' for the instance. A cap of 0 is no cap. Caps are
-- set by an administrator and visible to everyone they apply to.
CREATE TABLE ai_budgets (
  scope          TEXT NOT NULL CHECK (scope IN ('instance','user','project')),
  scope_id       TEXT NOT NULL DEFAULT '',
  max_cost_micros INTEGER NOT NULL DEFAULT 0 CHECK (max_cost_micros >= 0),
  max_tokens     INTEGER NOT NULL DEFAULT 0 CHECK (max_tokens >= 0),
  updated_by     TEXT,
  updated_at     TEXT NOT NULL,
  PRIMARY KEY (scope, scope_id)
) WITHOUT ROWID;
