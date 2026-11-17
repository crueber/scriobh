-- 1934 the progress email (F-509): a person's opt-in, and a third kind of message in the outbox.
--
-- mail_progress is shaped like mail_digest: the account address the opt-in was made for (a change of address stops
-- it), the period already handled (last_period: the week "2026-W10" or the month "2026-03" a message covered,
-- whether it sent or found nothing to say) and when it was turned on.
--
-- mail_outbox.kind has a CHECK list that cannot be changed in place, so the table is rebuilt with 'progress'. The
-- rows and the indexes are carried over unchanged; nothing references the table.

CREATE TABLE mail_progress (
  user_id     TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  frequency   TEXT NOT NULL CHECK (frequency IN ('weekly','monthly')),
  address     TEXT NOT NULL DEFAULT '',
  last_period TEXT NOT NULL DEFAULT '',
  enabled_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE mail_outbox_new (
  id               TEXT PRIMARY KEY,
  kind             TEXT NOT NULL CHECK (kind IN ('kindle','digest','progress')),
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
  window_since     TEXT NOT NULL DEFAULT '',
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

INSERT INTO mail_outbox_new (id, kind, person_id, project_id, recipient, recipient_domain, subject, body, file_name, file_type, file_bytes, file_sha256,
    dedupe_key, window_since, window_until, message_id, state, attempts, next_try_at, claimed_at, data_started_at, last_error, created_at, finished_at)
  SELECT id, kind, person_id, project_id, recipient, recipient_domain, subject, body, file_name, file_type, file_bytes, file_sha256,
    dedupe_key, window_since, window_until, message_id, state, attempts, next_try_at, claimed_at, data_started_at, last_error, created_at, finished_at
  FROM mail_outbox;

DROP TABLE mail_outbox;
ALTER TABLE mail_outbox_new RENAME TO mail_outbox;

CREATE INDEX mail_outbox_due ON mail_outbox(state, next_try_at);
CREATE INDEX mail_outbox_person ON mail_outbox(person_id, kind, created_at);
CREATE UNIQUE INDEX mail_outbox_dedupe ON mail_outbox(dedupe_key) WHERE dedupe_key <> '';
