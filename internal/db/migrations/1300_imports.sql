-- 1300 import provenance.
--
-- An import keeps no copy of the source and no job row: preview and apply are
-- stateless (apply re-sends the files and the token proves they are the ones
-- previewed). What is recorded is where a project came from and a hash of
-- every file that became a document, so a later, reviewed re-import can tell
-- what changed. import_jobs arrives with the Git import, which needs a clone to
-- keep between steps.

CREATE TABLE project_sources (
  id             TEXT PRIMARY KEY,
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind           TEXT NOT NULL CHECK (kind IN ('git','epub','docx','pdf','markdown')),
  source_url     TEXT NOT NULL DEFAULT '',
  source_path    TEXT NOT NULL DEFAULT '',   -- what the person called it: a folder name, never an absolute path
  source_hash    TEXT NOT NULL DEFAULT '',   -- the preview token: a hash of every accepted file's path and content
  commit_sha     TEXT NOT NULL DEFAULT '',
  metadata_json  TEXT NOT NULL DEFAULT '{}', -- layout, options, counts
  kept_copy_path TEXT NOT NULL DEFAULT '',
  imported_at    TEXT NOT NULL
);

CREATE INDEX project_sources_project ON project_sources(project_id, imported_at);

CREATE TABLE document_sources (
  document_id  TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  source_id    TEXT NOT NULL REFERENCES project_sources(id) ON DELETE CASCADE,
  source_path  TEXT NOT NULL,                -- the file the document came from (a split scene names its file)
  source_order INTEGER NOT NULL DEFAULT 0,
  source_hash  TEXT NOT NULL DEFAULT '',     -- sha256 of the file's bytes as read
  PRIMARY KEY (document_id)
);

CREATE INDEX document_sources_source ON document_sources(source_id, source_order);
