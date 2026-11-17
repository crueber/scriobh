-- 0440 suggestions: a reviewer's proposed change to a document's text, held
-- beside the text and never part of it until an editor accepts it. ops_json is
-- the ordered list of changes (each a range at base_revision, the text it holds,
-- the paragraph context around it and the replacement). The body of the
-- document is untouched while a suggestion is open.

CREATE TABLE suggestions (
  id               TEXT PRIMARY KEY,
  project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  document_id      TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  author_id        TEXT REFERENCES users(id) ON DELETE SET NULL,
  status           TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','accepted','rejected','withdrawn')),
  base_revision    INTEGER NOT NULL,
  ops_json         TEXT NOT NULL,
  note             TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  decided_at       TEXT,
  decided_by       TEXT REFERENCES users(id) ON DELETE SET NULL,
  applied_revision INTEGER,
  revision_id      TEXT
);

CREATE INDEX suggestions_project  ON suggestions(project_id, status, created_at);
CREATE INDEX suggestions_document ON suggestions(document_id, status, created_at);
CREATE INDEX suggestions_author   ON suggestions(project_id, author_id);
