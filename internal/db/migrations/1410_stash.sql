-- 1410 the stash: a project's overflow for paragraphs, notes and outlines that
-- are not in the manuscript. It is not part of any document: not in the
-- manuscript search, the word counts, the outline or an export. It is part of
-- a backup (it is in the database) and of the mirror (a stash/ folder). Search
-- is in 1420. Only editors and owners reach it, reads included.

CREATE TABLE stash_items (
  id                 TEXT PRIMARY KEY,
  project_id         TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_document_id TEXT REFERENCES documents(id) ON DELETE SET NULL,
  kind               TEXT NOT NULL DEFAULT 'paragraph' CHECK (kind IN ('paragraph','note','outline')),
  title              TEXT NOT NULL DEFAULT '',
  content            TEXT NOT NULL DEFAULT '',     -- Markdown
  tags               TEXT NOT NULL DEFAULT '',     -- a JSON array of strings, '' for none
  position           REAL NOT NULL DEFAULT 65536,
  revision           INTEGER NOT NULL DEFAULT 1,   -- the client's concurrency token
  word_count         INTEGER NOT NULL DEFAULT 0,
  content_hash       TEXT NOT NULL DEFAULT '',
  created_by         TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  deleted_at         TEXT
);

CREATE INDEX stash_items_project ON stash_items(project_id, deleted_at, position);
CREATE INDEX stash_items_source  ON stash_items(source_document_id) WHERE source_document_id IS NOT NULL;
