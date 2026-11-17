-- 1310 import jobs: a repository import keeps its clone between preview and apply.
--
-- The folder import (1300) is stateless. A repository import is not: the clone
-- is data/imports/<id>/ until apply moves it next to the new project as its
-- provenance copy. A job that is never applied is removed after a day.

CREATE TABLE import_jobs (
  id           TEXT PRIMARY KEY,
  owner_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id   TEXT REFERENCES projects(id) ON DELETE SET NULL,   -- NULL until the project exists
  kind         TEXT NOT NULL DEFAULT 'git' CHECK (kind IN ('git','epub','docx','pdf','markdown')),
  source_url   TEXT NOT NULL,
  branch       TEXT NOT NULL DEFAULT '',
  commit_sha   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL CHECK (status IN ('preview','running','done','failed')),
  token        TEXT NOT NULL DEFAULT '',                          -- the preview token of the files in the clone
  clone_path   TEXT NOT NULL DEFAULT '',                          -- relative to the data directory
  report_json  TEXT NOT NULL DEFAULT '{}',                        -- layout, counts, skipped
  created_at   TEXT NOT NULL,
  finished_at  TEXT,
  error        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX import_jobs_owner ON import_jobs(owner_id, created_at);
