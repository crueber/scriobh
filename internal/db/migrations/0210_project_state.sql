-- 0210 project state: archiving, and what each person last did with a project.

ALTER TABLE projects ADD COLUMN archived_at TEXT;

-- Per-person state: pinned shelf order, "recently opened", and the document to
-- resume in ("Continue where I left off").
CREATE TABLE user_project_state (
  user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  pinned           INTEGER NOT NULL DEFAULT 0,
  last_opened_at   TEXT,
  last_document_id TEXT,
  PRIMARY KEY (user_id, project_id)
);

CREATE INDEX user_project_state_recent ON user_project_state(user_id, last_opened_at);
