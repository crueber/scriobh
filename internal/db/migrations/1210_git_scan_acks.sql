-- 1210 Git sync: acknowledged pre-push findings.
--
-- The check for secrets before a push is a safety net, and manuscripts quote
-- keys. The owner of a project can acknowledge a finding for one file with one
-- exact content: the row names the file and the SHA-256 of what was looked at,
-- so a changed file is checked again. Each acknowledgement is audited.

CREATE TABLE git_scan_acks (
  remote_id TEXT NOT NULL REFERENCES git_remotes(id) ON DELETE CASCADE,
  path      TEXT NOT NULL,
  sha256    TEXT NOT NULL,
  rule      TEXT NOT NULL DEFAULT '',
  acked_by  TEXT REFERENCES users(id) ON DELETE SET NULL,
  acked_at  TEXT NOT NULL,
  PRIMARY KEY (remote_id, path, sha256)
);
