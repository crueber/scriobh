-- 0340 research notes and clippings. They live in their own table, not in
-- documents: documents.project_id is required and a Compendium can exist with no
-- project, and a research note must never appear in the manuscript tree, the
-- outline, the word counts or the compile. A note belongs to exactly one owner,
-- a project (project-local research) or a Compendium (shared research).
-- The tree, trash and revision counter work like documents'; revision history
-- and wiki links are not built for research.

CREATE TABLE research_notes (
  id            TEXT PRIMARY KEY,
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,
  compendium_id TEXT REFERENCES compendiums(id) ON DELETE CASCADE,
  parent_id     TEXT REFERENCES research_notes(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL DEFAULT 'note' CHECK (kind IN ('note','clipping','folder')),
  title         TEXT NOT NULL DEFAULT '',
  body          TEXT NOT NULL DEFAULT '',         -- Markdown
  -- A clipping is pasted or typed by the person. The server never fetches the
  -- address: it is kept as entered, with the date the person read it and the
  -- passage they kept.
  source_url    TEXT NOT NULL DEFAULT '',
  retrieved_on  TEXT NOT NULL DEFAULT '',         -- YYYY-MM-DD, empty when unknown
  quote         TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'inbox' CHECK (status IN ('inbox','reading','useful','archived')),
  position      REAL NOT NULL DEFAULT 65536,
  revision      INTEGER NOT NULL DEFAULT 1,       -- the client's concurrency token
  word_count    INTEGER NOT NULL DEFAULT 0,
  content_hash  TEXT NOT NULL DEFAULT '',
  created_by    TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT,
  trash_batch   TEXT,
  CHECK ((project_id IS NULL) <> (compendium_id IS NULL))
);

CREATE INDEX research_project_tree ON research_notes(project_id, parent_id, position) WHERE project_id IS NOT NULL;
CREATE INDEX research_compendium_tree ON research_notes(compendium_id, parent_id, position) WHERE compendium_id IS NOT NULL;
CREATE INDEX research_project_status ON research_notes(project_id, status) WHERE project_id IS NOT NULL;
CREATE INDEX research_compendium_status ON research_notes(compendium_id, status) WHERE compendium_id IS NOT NULL;
CREATE INDEX research_parent ON research_notes(parent_id) WHERE parent_id IS NOT NULL;
