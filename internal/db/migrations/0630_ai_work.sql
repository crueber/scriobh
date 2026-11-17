-- 0630 What the AI recipes keep. Unlike ai_runs, these rows hold text derived
-- from the manuscript (a suggestion, a note's quote, a mark's range), so they
-- belong to the project and the person and go with them: deleting a project, a
-- document or a person deletes what is kept here. The core's rule stands: a run
-- stores no prompt and no answer; what follows is the explicit, short-lived or
-- writer-chosen exception for each feature, and each has a way out.

-- A suggestion waiting to be accepted. Held for a short window (30 minutes), so
-- that "accept" applies a text the server produced and the caller cannot
-- supply. Expired rows are removed the next time any AI recipe runs or is
-- accepted; an expired row is never read.
CREATE TABLE ai_pending (
  run_id       TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  document_id  TEXT REFERENCES documents(id) ON DELETE CASCADE,
  recipe       TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('edit','alternatives','summary','chat')),
  doc_revision INTEGER NOT NULL DEFAULT 0,
  output_hash  TEXT NOT NULL,              -- SHA-256 of the run's answer; ai_runs.output_hash holds the same
  items_json   TEXT NOT NULL,              -- the suggestions, each with the hash of its own text
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL
);
CREATE INDEX ai_pending_expiry ON ai_pending(expires_at);
CREATE INDEX ai_pending_user ON ai_pending(user_id, project_id);

-- The accept ledger: one row for each suggestion that was accepted or rejected,
-- written in the same transaction as the write it records. It holds no text, so
-- it outlives the pending row and keeps accept idempotent.
CREATE TABLE ai_accepts (
  run_id      TEXT NOT NULL,
  item_id     TEXT NOT NULL,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  action      TEXT NOT NULL CHECK (action IN ('accepted','rejected')),
  revision    INTEGER NOT NULL DEFAULT 0,  -- the document revision the accept produced
  revision_id TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  PRIMARY KEY (run_id, item_id)
) WITHOUT ROWID;

-- A review: the notes of a critique, the findings of a continuity audit or the
-- sheets an extraction proposed, kept for the person who ran it until they
-- delete it. Private to that person.
CREATE TABLE ai_reviews (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  document_id  TEXT REFERENCES documents(id) ON DELETE CASCADE,
  run_id       TEXT NOT NULL,
  recipe       TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('critique','continuity','extraction')),
  doc_revision INTEGER NOT NULL DEFAULT 0,
  dropped      INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL
);
CREATE INDEX ai_reviews_user ON ai_reviews(user_id, project_id, created_at);
CREATE INDEX ai_reviews_doc ON ai_reviews(document_id);

CREATE TABLE ai_notes (
  id          TEXT PRIMARY KEY,
  review_id   TEXT NOT NULL REFERENCES ai_reviews(id) ON DELETE CASCADE,
  position    INTEGER NOT NULL,
  category    TEXT NOT NULL DEFAULT 'other',
  severity    TEXT NOT NULL DEFAULT 'medium' CHECK (severity IN ('low','medium','high')),
  body        TEXT NOT NULL DEFAULT '',
  example     TEXT NOT NULL DEFAULT '',
  trimmed     INTEGER NOT NULL DEFAULT 0,
  quote       TEXT NOT NULL DEFAULT '',
  before_ctx  TEXT NOT NULL DEFAULT '',
  after_ctx   TEXT NOT NULL DEFAULT '',
  start_u16   INTEGER NOT NULL DEFAULT -1,  -- -1: the quote was not found when the note was made
  end_u16     INTEGER NOT NULL DEFAULT -1,
  at_revision INTEGER NOT NULL DEFAULT 0,
  sources_json TEXT NOT NULL DEFAULT '[]',
  sheet_json  TEXT NOT NULL DEFAULT '',     -- extraction: the proposed entity
  status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','accepted','dismissed')),
  result_ref  TEXT NOT NULL DEFAULT '',     -- extraction: the entity that was created
  resolved_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ai_notes_review ON ai_notes(review_id, position);

-- The Lab's marks: a range of a document, why it was marked and, for a fix or a
-- trim, the exact edit. Each is accepted or dismissed on its own. A mark made
-- by a local analyser has source 'local' and a run id that begins lab_.
CREATE TABLE ai_marks (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  run_id      TEXT NOT NULL,
  source      TEXT NOT NULL CHECK (source IN ('model','local')),
  kind        TEXT NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  quote       TEXT NOT NULL,
  before_ctx  TEXT NOT NULL DEFAULT '',
  after_ctx   TEXT NOT NULL DEFAULT '',
  start_u16   INTEGER NOT NULL,
  end_u16     INTEGER NOT NULL,
  at_revision INTEGER NOT NULL,
  has_fix     INTEGER NOT NULL DEFAULT 0,   -- 0: an observation; 1: fix is the exact replacement ('' deletes)
  fix         TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','accepted','dismissed')),
  created_at  TEXT NOT NULL,
  resolved_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ai_marks_doc ON ai_marks(document_id, user_id, status);
CREATE INDEX ai_marks_run ON ai_marks(run_id);

-- A rolling summary job: the chapters in the order they will be summarised and
-- how far it has got. The client drives it one chapter at a time (every send is
-- previewed), so the row only remembers the plan and the position.
CREATE TABLE ai_jobs (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('rolling_summary')),
  status      TEXT NOT NULL CHECK (status IN ('open','done','cancelled')),
  plan_json   TEXT NOT NULL,                -- [{"document_id":..,"state":"todo|done|skipped","run_id":..}]
  estimate_json TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX ai_jobs_project ON ai_jobs(project_id, user_id, status);

-- A kept conversation. Off by default: a chat is held by the browser unless the
-- person asks to keep it, and then it is theirs alone, with a delete.
CREATE TABLE ai_chats (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX ai_chats_user ON ai_chats(user_id, project_id, updated_at);

CREATE TABLE ai_chat_turns (
  chat_id    TEXT NOT NULL REFERENCES ai_chats(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  role       TEXT NOT NULL CHECK (role IN ('user','assistant')),
  content    TEXT NOT NULL,
  run_id     TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY (chat_id, seq)
) WITHOUT ROWID;

-- A person's own default provider chains per model class (cheap, strong, fast,
-- local). The project's and the instance's live in the policy and the settings;
-- resolution is: the request's own choice, the project, the person, the
-- instance.
CREATE TABLE ai_user_defaults (
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  class      TEXT NOT NULL CHECK (class IN ('cheap','strong','fast','local')),
  chain_json TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (user_id, class)
) WITHOUT ROWID;
