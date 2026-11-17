-- 1932 gentle nudges (F-508): what has been shown, so that none is shown twice and only a few a day.
-- A row is the id of a nudge on a local day. Rows older than 60 days are removed when a new one is written.

CREATE TABLE studio_nudges_seen (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  day     TEXT NOT NULL,
  nudge   TEXT NOT NULL,
  PRIMARY KEY (user_id, day, nudge)
) WITHOUT ROWID;
