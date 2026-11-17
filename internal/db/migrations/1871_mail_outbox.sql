-- 1871 the mail outbox.
--
-- A row is one message for one person. subject, body and the recipient are blanked when the row finishes
-- (sent, dead or cancelled): what stays is the kind, the state, the failure class and the recipient's domain,
-- for the rate limits, the dedupe keys and the administrator's view. last_error is a class, never the text a
-- relay said. An attachment is a 0600 file under the data directory named by the row id, never a blob here.
-- message_id is fixed when the row is made so every attempt carries the same Message-ID.
CREATE TABLE mail_outbox (
  id               TEXT PRIMARY KEY,
  kind             TEXT NOT NULL CHECK (kind IN ('kindle','digest')),
  person_id        TEXT NOT NULL,
  project_id       TEXT NOT NULL DEFAULT '',
  recipient        TEXT NOT NULL,
  recipient_domain TEXT NOT NULL DEFAULT '',
  subject          TEXT NOT NULL DEFAULT '',
  body             TEXT NOT NULL DEFAULT '',
  file_name        TEXT NOT NULL DEFAULT '',
  file_type        TEXT NOT NULL DEFAULT '',
  file_bytes       INTEGER NOT NULL DEFAULT 0,
  file_sha256      TEXT NOT NULL DEFAULT '',
  dedupe_key       TEXT NOT NULL DEFAULT '',
  window_since     TEXT NOT NULL DEFAULT '', -- a digest: the notifications it was made from; it is made again from them at send time
  window_until     TEXT NOT NULL DEFAULT '',
  message_id       TEXT NOT NULL,
  state            TEXT NOT NULL CHECK (state IN ('pending','sending','sent','dead')),
  attempts         INTEGER NOT NULL DEFAULT 0,
  next_try_at      TEXT NOT NULL,
  claimed_at       TEXT NOT NULL DEFAULT '',
  data_started_at  TEXT NOT NULL DEFAULT '',
  last_error       TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  finished_at      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX mail_outbox_due ON mail_outbox(state, next_try_at);
CREATE INDEX mail_outbox_person ON mail_outbox(person_id, kind, created_at);
-- A dedupe key is used once, whatever became of the message.
CREATE UNIQUE INDEX mail_outbox_dedupe ON mail_outbox(dedupe_key) WHERE dedupe_key <> '';
