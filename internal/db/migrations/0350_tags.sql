-- 0350 tags. A tag belongs to one project or one Compendium and is unique by
-- name within it (compared folded, in Go: SQLite's lower() is ASCII-only). The
-- colour is one of a fixed list of tokens, so both themes can design it.
-- document_tags is the join for manuscript documents (created now so a later
-- round adds the ops without a migration); research_tags is the one in use.

CREATE TABLE tags (
  id            TEXT PRIMARY KEY,
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,
  compendium_id TEXT REFERENCES compendiums(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  name_key      TEXT NOT NULL,
  color         TEXT NOT NULL DEFAULT 'slate' CHECK (color IN ('slate','clay','ochre','moss','teal','blue','plum','rose')),
  created_at    TEXT NOT NULL,
  CHECK ((project_id IS NULL) <> (compendium_id IS NULL))
);

CREATE UNIQUE INDEX tags_project_name ON tags(project_id, name_key) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX tags_compendium_name ON tags(compendium_id, name_key) WHERE compendium_id IS NOT NULL;

CREATE TABLE document_tags (
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  tag_id      TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (document_id, tag_id)
);

CREATE INDEX document_tags_tag ON document_tags(tag_id);

CREATE TABLE research_tags (
  note_id TEXT NOT NULL REFERENCES research_notes(id) ON DELETE CASCADE,
  tag_id  TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (note_id, tag_id)
);

CREATE INDEX research_tags_tag ON research_tags(tag_id);
