-- 0700 goals and writing stats (Phase 2, goals v1).
--
-- Goals count WORDS ADDED. Net words are stored beside them and never fail a
-- day. Everything here is per person: other members of a project never see
-- these rows.

CREATE TABLE goals (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('daily','weekly','monthly','project','document')),
  target_words INTEGER NOT NULL CHECK (target_words > 0),
  basis        TEXT NOT NULL DEFAULT 'added' CHECK (basis IN ('added','net')),
  start_date   TEXT NOT NULL DEFAULT '',   -- local date the goal was set, YYYY-MM-DD
  end_date     TEXT NOT NULL DEFAULT '',   -- optional last local date; '' for none
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

-- v1 holds one daily and one project goal per person per project. Later kinds
-- (weekly, monthly, document, several at once) are not constrained here.
CREATE UNIQUE INDEX goals_one ON goals(user_id, project_id, kind) WHERE kind IN ('daily','project');
CREATE INDEX goals_project ON goals(project_id);

-- A session is a run of saves by one person in one project with no gap longer
-- than 30 minutes and no change of local day or time zone. It belongs to
-- exactly one local day, recorded at write time, so a later change of zone
-- never re-buckets history and daily_stats is a plain GROUP BY of sessions.
CREATE TABLE writing_sessions (
  id             TEXT PRIMARY KEY,
  user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  document_id    TEXT,                      -- the first document saved; a session may span several
  day            TEXT NOT NULL,             -- local date in the person's zone when the session began
  tz             TEXT NOT NULL,             -- the zone that date was read in
  started_at     TEXT NOT NULL,
  ended_at       TEXT NOT NULL,             -- the last save
  words_added    INTEGER NOT NULL DEFAULT 0,
  words_net      INTEGER NOT NULL DEFAULT 0,
  active_seconds INTEGER NOT NULL DEFAULT 0, -- sum of the gaps between saves, each capped at 5 minutes
  saves          INTEGER NOT NULL DEFAULT 0,
  kind           TEXT NOT NULL DEFAULT 'writing' CHECK (kind IN ('writing','sprint'))
);

CREATE INDEX writing_sessions_recent ON writing_sessions(user_id, project_id, ended_at);
CREATE INDEX writing_sessions_day ON writing_sessions(user_id, project_id, day);

-- The text a document had when a session first saved it, kept only while the
-- session is the person's latest: credit for a document is measured against
-- this baseline, so a cut pasted back, or a word grown letter by letter across
-- saves, is not credited twice. Rows are removed when the next session starts.
CREATE TABLE writing_session_docs (
  session_id     TEXT NOT NULL REFERENCES writing_sessions(id) ON DELETE CASCADE,
  document_id    TEXT NOT NULL,
  baseline_text  TEXT NOT NULL,
  baseline_words INTEGER NOT NULL DEFAULT 0,
  carried        INTEGER NOT NULL DEFAULT 0, -- credit earned before the baseline was last reset
  credit         INTEGER NOT NULL DEFAULT 0, -- total credit for this document in this session
  PRIMARY KEY (session_id, document_id)
);

CREATE TABLE daily_stats (
  user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  day            TEXT NOT NULL,             -- local date, YYYY-MM-DD
  words_added    INTEGER NOT NULL DEFAULT 0,
  words_net      INTEGER NOT NULL DEFAULT 0,
  minutes        INTEGER NOT NULL DEFAULT 0,
  sessions       INTEGER NOT NULL DEFAULT 0,
  active_seconds INTEGER NOT NULL DEFAULT 0,
  goal_target    INTEGER NOT NULL DEFAULT 0, -- the daily target in force that day; frozen once the day is hit
  goal_hit       INTEGER NOT NULL DEFAULT 0 CHECK (goal_hit IN (0,1)),
  updated_at     TEXT NOT NULL,
  PRIMARY KEY (user_id, project_id, day)
);

CREATE INDEX daily_stats_hits ON daily_stats(user_id, project_id, goal_hit, day);
