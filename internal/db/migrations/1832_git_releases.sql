-- 1832 Releases: a compiled edition published as a release asset after a push. A row is one attempt; it is the
-- record that makes a retry idempotent (same remote, tag and input hash) and the job a screen polls, because a
-- compile and an upload are longer than a request may take.
CREATE TABLE git_releases (
  id            TEXT PRIMARY KEY,
  remote_id     TEXT NOT NULL REFERENCES git_remotes(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  tag           TEXT NOT NULL,
  format        TEXT NOT NULL,              -- epub, docx, pdf, md or md_bundle
  preset        TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL CHECK (status IN ('running','ok','error')),
  commit_sha    TEXT NOT NULL DEFAULT '',   -- the commit the tag points at
  input_hash    TEXT NOT NULL DEFAULT '',   -- the compile's manifest hash: what makes two editions the same
  file_sha256   TEXT NOT NULL DEFAULT '',
  asset_name    TEXT NOT NULL DEFAULT '',
  asset_bytes   INTEGER NOT NULL DEFAULT 0,
  reused        INTEGER NOT NULL DEFAULT 0, -- the asset was already there: nothing was uploaded
  release_url   TEXT NOT NULL DEFAULT '',   -- the host's page for the release (shown, never followed)
  error_code    TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  started_by    TEXT REFERENCES users(id) ON DELETE SET NULL,
  started_at    TEXT NOT NULL,
  finished_at   TEXT
);

CREATE INDEX git_releases_remote ON git_releases(remote_id, started_at);
-- One attempt in flight per remote and tag, enforced by the database.
CREATE UNIQUE INDEX git_releases_one_running ON git_releases(remote_id, tag) WHERE status = 'running';
