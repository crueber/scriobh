-- 1420 stash search. Like doc_text and research_text: the plain text the index
-- reads, written by the stash ops in the same transaction as the item, with an
-- external-content FTS5 table that follows it through triggers. Rebuild with
-- `scriobh search rebuild`. Nothing here is read by the manuscript search.

CREATE TABLE stash_text (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id    TEXT NOT NULL UNIQUE REFERENCES stash_items(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  tags       TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX stash_text_project ON stash_text(project_id);

CREATE VIRTUAL TABLE stash_fts USING fts5(
  title, tags, body,
  content = 'stash_text', content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER stash_text_ai AFTER INSERT ON stash_text BEGIN
  INSERT INTO stash_fts(rowid, title, tags, body) VALUES (new.id, new.title, new.tags, new.body);
END;

CREATE TRIGGER stash_text_ad AFTER DELETE ON stash_text BEGIN
  INSERT INTO stash_fts(stash_fts, rowid, title, tags, body) VALUES ('delete', old.id, old.title, old.tags, old.body);
END;

CREATE TRIGGER stash_text_au AFTER UPDATE ON stash_text BEGIN
  INSERT INTO stash_fts(stash_fts, rowid, title, tags, body) VALUES ('delete', old.id, old.title, old.tags, old.body);
  INSERT INTO stash_fts(rowid, title, tags, body) VALUES (new.id, new.title, new.tags, new.body);
END;
