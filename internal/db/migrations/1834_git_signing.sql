-- 1834 The project's SSH signing key: an ed25519 key that signs the commits of the remotes that ask for it. It is
-- not a deploy key (a deploy key is one repository's, and changes when an address does); it is the project's, so
-- one allowed-signers line verifies every signed commit of every remote. The private half is sealed with the
-- instance key, never returned, never in a backup of the repository, an export or a push.
CREATE TABLE git_signing_keys (
  project_id   TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  public_key   TEXT NOT NULL,               -- an authorized_keys line
  fingerprint  TEXT NOT NULL,
  key_sealed   TEXT NOT NULL,
  created_by   TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at   TEXT NOT NULL
);
