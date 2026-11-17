-- 1852 approved keys for the project passphrase gate (F-252).
--
-- "Accept API keys" on a protected project is per key, not per project: a key passes the gate only if it is listed here for
-- the epoch the gate is in. A key is listed when it is made by a session that had the project unlocked, or when the
-- project's owner approves it by id (project.passphrase.set, approve_keys). A new passphrase moves the epoch on, which
-- voids every approval at once, as it voids every unlock. A key made earlier by a member who was never given the
-- passphrase is therefore never let in by the owner switching keys on.
CREATE TABLE project_gate_key_ok (
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  key_id      TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  epoch       INTEGER NOT NULL,
  how         TEXT NOT NULL CHECK (how IN ('minted','owner')),
  approved_by TEXT,
  created_at  TEXT NOT NULL,
  PRIMARY KEY (project_id, key_id)
);

CREATE INDEX project_gate_key_ok_key ON project_gate_key_ok(key_id);
