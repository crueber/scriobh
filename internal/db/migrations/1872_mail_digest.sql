-- 1872 the email digest: a person's opt-in. last_period is the period (a day, or an ISO week) already
-- handled, whether it sent or found nothing to say; last_cursor is where the next digest's window starts, so a
-- digest only ever counts what happened after the person turned it on or the last one ended.
CREATE TABLE mail_digest (
  user_id     TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  frequency   TEXT NOT NULL CHECK (frequency IN ('daily','weekly')),
  last_period TEXT NOT NULL DEFAULT '',
  last_cursor TEXT NOT NULL,
  enabled_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
