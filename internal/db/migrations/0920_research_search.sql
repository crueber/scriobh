-- 0920 research search. Like doc_text and entity_text: the plain text the index
-- reads, written by the research ops in the same transaction as the note, with
-- an external-content FTS5 table that follows it through triggers. Rebuild with
-- `scriobh search rebuild`. A project's search never reads this table for a
-- Compendium's notes: the query is scoped by owner.

CREATE TABLE research_text (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  note_id       TEXT NOT NULL UNIQUE REFERENCES research_notes(id) ON DELETE CASCADE,
  project_id    TEXT,
  compendium_id TEXT,
  title         TEXT NOT NULL DEFAULT '',
  source        TEXT NOT NULL DEFAULT '',
  body          TEXT NOT NULL DEFAULT ''
);

CREATE INDEX research_text_project ON research_text(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX research_text_compendium ON research_text(compendium_id) WHERE compendium_id IS NOT NULL;

CREATE VIRTUAL TABLE research_fts USING fts5(
  title, source, body,
  content = 'research_text', content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER research_text_ai AFTER INSERT ON research_text BEGIN
  INSERT INTO research_fts(rowid, title, source, body) VALUES (new.id, new.title, new.source, new.body);
END;

CREATE TRIGGER research_text_ad AFTER DELETE ON research_text BEGIN
  INSERT INTO research_fts(research_fts, rowid, title, source, body) VALUES ('delete', old.id, old.title, old.source, old.body);
END;

CREATE TRIGGER research_text_au AFTER UPDATE ON research_text BEGIN
  INSERT INTO research_fts(research_fts, rowid, title, source, body) VALUES ('delete', old.id, old.title, old.source, old.body);
  INSERT INTO research_fts(rowid, title, source, body) VALUES (new.id, new.title, new.source, new.body);
END;
