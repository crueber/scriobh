-- 0900 search. SQLite cannot derive plain text from Markdown, so the application
-- writes doc_text (title, synopsis, plain body) in the same transaction as the
-- document, and an external-content FTS5 table follows doc_text through
-- triggers. Rebuild with `scriobh search rebuild`.

CREATE TABLE doc_text (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  document_id TEXT NOT NULL UNIQUE REFERENCES documents(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  synopsis    TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX doc_text_project ON doc_text(project_id);

CREATE VIRTUAL TABLE doc_fts USING fts5(
  title, synopsis, body,
  content = 'doc_text', content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER doc_text_ai AFTER INSERT ON doc_text BEGIN
  INSERT INTO doc_fts(rowid, title, synopsis, body) VALUES (new.id, new.title, new.synopsis, new.body);
END;

CREATE TRIGGER doc_text_ad AFTER DELETE ON doc_text BEGIN
  INSERT INTO doc_fts(doc_fts, rowid, title, synopsis, body) VALUES ('delete', old.id, old.title, old.synopsis, old.body);
END;

CREATE TRIGGER doc_text_au AFTER UPDATE ON doc_text BEGIN
  INSERT INTO doc_fts(doc_fts, rowid, title, synopsis, body) VALUES ('delete', old.id, old.title, old.synopsis, old.body);
  INSERT INTO doc_fts(rowid, title, synopsis, body) VALUES (new.id, new.title, new.synopsis, new.body);
END;
