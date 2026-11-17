-- 1931 writing sessions and sprints (F-152, F-507).
--
-- A sprint is a window of time on one project. The words in it are the same additions the save loop records for
-- the day (record.go adds each save's credit to the day and, in the same transaction, to the sprint running for
-- that person and project): a sprint holds the words recorded while it ran, each also counted once in the day it was
-- written on, and a sprint that runs past local midnight holds the words of two days.
--
-- A sprint left running is closed by a rule of the stored times (closes_at), not by a timer: a save after
-- closes_at is not added, a read treats the row as closed, and the next write that touches the person's sprints
-- (or a restore) settles it. Nothing is lost, and nothing is called a failure.

CREATE TABLE studio_sprints (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  day             TEXT NOT NULL,          -- the person's local date when it began
  tz              TEXT NOT NULL,
  planned_minutes INTEGER NOT NULL DEFAULT 0, -- 0: an open session timer
  target_words    INTEGER NOT NULL DEFAULT 0, -- 0: none
  started_at      TEXT NOT NULL,
  closes_at       TEXT NOT NULL,          -- saves after this are not added
  state           TEXT NOT NULL CHECK (state IN ('running','completed','stopped','abandoned')),
  ended_at        TEXT NOT NULL DEFAULT '',
  words_added     INTEGER NOT NULL DEFAULT 0,
  words_net       INTEGER NOT NULL DEFAULT 0,
  saves           INTEGER NOT NULL DEFAULT 0
);

-- One sprint at a time per person; and the save path finds it with one probe.
CREATE UNIQUE INDEX studio_sprints_running ON studio_sprints(user_id) WHERE state = 'running';
CREATE INDEX studio_sprints_user ON studio_sprints(user_id, started_at);
