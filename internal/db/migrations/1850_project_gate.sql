-- 1850 project passphrase gate (F-252).
--
-- A project's owner may protect it with a passphrase. It is a lock for the screen, not encryption: the text is
-- in the database as it always was. While a gate exists a person must unlock the project in their session before
-- anything of it is served (internal/domain/projectgate, internal/ops/gate.go).
--
-- The passphrase is hashed with argon2id by the same code as a share link's (share.HashPassphrase) and never
-- stored or logged in the clear. The hash has its own table, not a column of projects: no project query can
-- select it by accident, and a project with no gate has no row.

CREATE TABLE project_gates (
  project_id   TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  pass_hash    TEXT NOT NULL,
  -- epoch counts passphrase changes. An unlock is valid only for the epoch it was made in, so changing the
  -- passphrase relocks every session at once.
  epoch        INTEGER NOT NULL DEFAULT 1,
  -- an unlock ends after this many idle minutes (never more than 12 hours from the unlock)
  idle_minutes INTEGER NOT NULL DEFAULT 30 CHECK (idle_minutes BETWEEN 1 AND 720),
  -- 0: an API key is refused while the passphrase is set; 1: a key is its own credential and passes
  allow_keys   INTEGER NOT NULL DEFAULT 0,
  set_by       TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

-- An unlock lives on a session, not on a person: another browser, another device, or the same person after
-- signing out and in, is locked again. The row goes with the session (cascade) or the project; the gate also
-- ignores a row whose session has been revoked.
CREATE TABLE project_unlocks (
  session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  epoch        INTEGER NOT NULL,
  unlocked_at  TEXT NOT NULL,
  last_used_at TEXT NOT NULL,
  PRIMARY KEY (session_id, project_id)
);

CREATE INDEX project_unlocks_project ON project_unlocks(project_id);

-- Wrong passphrases, counted per project and per bucket: the session ("ses:"), the person ("user:", so signing
-- out and in does not reset it) and the client address ("ip:", a keyed hash, never the address). The first three
-- wrong tries in a row cost nothing; each later one earns a longer wait (30 seconds, doubling, at most an hour)
-- before that bucket may try again, even with the right passphrase.
CREATE TABLE project_unlock_failures (
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  bucket       TEXT NOT NULL,
  failed       INTEGER NOT NULL,
  locked_until TEXT,
  last_at      TEXT NOT NULL,
  PRIMARY KEY (project_id, bucket)
);

-- What each API key has done to a gated project: its calls that passed (the owner allows keys) and its calls that
-- were refused. The owner sees it in project.passphrase.get, the key's owner in key.list.
CREATE TABLE project_gate_key_use (
  key_id     TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  uses       INTEGER NOT NULL DEFAULT 0,
  refused    INTEGER NOT NULL DEFAULT 0,
  first_at   TEXT NOT NULL,
  last_at    TEXT NOT NULL,
  PRIMARY KEY (key_id, project_id)
);

CREATE INDEX project_gate_key_use_project ON project_gate_key_use(project_id);
