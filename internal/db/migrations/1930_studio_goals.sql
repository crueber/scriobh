-- 1930 writing studio: goals with a history (F-500, F-506, F-515).
--
-- The v1 goals table (0700) keeps one daily and one project goal per person per project and is edited in place;
-- it stays, with its ops, for the Goals page. These tables hold the goals of the studio: daily, weekly, monthly
-- and total goals, measured in words, sessions, chapters or minutes, for a person (project_id empty: every project
-- they may read) or for one project. A goal is never edited in place: a change adds a version, and a past period is
-- judged against the version that was in force then.
--
-- Goals count words ADDED. Nothing here is a net-words rule.

CREATE TABLE studio_goals (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL DEFAULT '',  -- '' for every project the person may read
  period      TEXT NOT NULL CHECK (period IN ('day','week','month','total')),
  measure     TEXT NOT NULL CHECK (measure IN ('words','sessions','chapters','minutes')),
  start_date  TEXT NOT NULL,             -- the person's local date the goal was set, YYYY-MM-DD
  ended_on    TEXT NOT NULL DEFAULT '',  -- the local date it was archived; the last date it applied is this date if that period was met by what the reader may count, else the day before (worked out when read)
  archived_at TEXT NOT NULL DEFAULT '',
  -- A total goal, once reached, stays completed: the local date it was first seen reached, never cleared by a cut (only by
  -- the person raising the target). Written only from a read that counts every project the person has written in.
  completed_on TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);

CREATE INDEX studio_goals_user ON studio_goals(user_id, archived_at);
-- A project goal goes with its project.
CREATE INDEX studio_goals_project ON studio_goals(project_id) WHERE project_id <> '';
CREATE TRIGGER studio_goals_project_gone AFTER DELETE ON projects
BEGIN
  DELETE FROM studio_goals WHERE project_id = OLD.id;
END;

-- One row per change. from_date is the local date it took effect (the date of created_at in the person's zone at
-- the time); progress_at is the person's progress in the period it took effect in, at that moment, so that a
-- period in which the target changed is judged without guessing what was written before the change: a period is met
-- if the total reached the target in force at some point in it (a raise after the goal was met never takes it away).
CREATE TABLE studio_goal_versions (
  goal_id     TEXT NOT NULL REFERENCES studio_goals(id) ON DELETE CASCADE,
  version     INTEGER NOT NULL,
  target      INTEGER NOT NULL CHECK (target > 0),
  deadline    TEXT NOT NULL DEFAULT '',  -- a total goal's optional deadline, YYYY-MM-DD; changed with a new version like the target
  from_date   TEXT NOT NULL,
  progress_at INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  PRIMARY KEY (goal_id, version)
) WITHOUT ROWID;

-- A chapter finished (its status set to done) for the first time by a person: what a chapters goal counts.
CREATE TABLE studio_chapters_done (
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  day         TEXT NOT NULL,             -- local date, YYYY-MM-DD
  created_at  TEXT NOT NULL,
  PRIMARY KEY (user_id, document_id)
) WITHOUT ROWID;

CREATE INDEX studio_chapters_done_day ON studio_chapters_done(user_id, day);
