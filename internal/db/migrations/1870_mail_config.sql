-- 1870 mail: the operator's relay. One row, off by default.
--
-- Nothing is sent unless an administrator has switched mail on and a person has switched on the one feature
-- that sends (a Kindle delivery they asked for, the digest they opted in to). The relay password is sealed
-- with the instance key (internal/secrets), bound to this row, the relay's address and the user name, and is
-- never returned. restore_paused is its own mark: a restore switches mail off and only an administrator
-- switching it on again clears it.
CREATE TABLE mail_config (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  enabled           INTEGER NOT NULL DEFAULT 0,
  host              TEXT NOT NULL DEFAULT '',
  port              INTEGER NOT NULL DEFAULT 0,
  security          TEXT NOT NULL DEFAULT 'starttls' CHECK (security IN ('starttls','tls','none')),
  allow_private     INTEGER NOT NULL DEFAULT 0,
  tls_ca_pem        TEXT NOT NULL DEFAULT '',
  username          TEXT NOT NULL DEFAULT '',
  password_sealed   TEXT NOT NULL DEFAULT '',
  from_address      TEXT NOT NULL DEFAULT '',
  from_name         TEXT NOT NULL DEFAULT '',
  max_attachment_mb INTEGER NOT NULL DEFAULT 12,
  disabled_reason   TEXT NOT NULL DEFAULT '',
  restore_paused    INTEGER NOT NULL DEFAULT 0,
  last_test_at      TEXT NOT NULL DEFAULT '',
  last_test_class   TEXT NOT NULL DEFAULT '',
  updated_by        TEXT,
  updated_at        TEXT NOT NULL DEFAULT ''
);
