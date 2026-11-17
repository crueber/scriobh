-- 0910 entity search. Like doc_text, entity_text holds the plain text the index
-- reads (name, aliases, summary, and the body with its Markdown removed). The
-- entity ops write it in the same transaction as the entity; an external
-- content FTS5 table follows it through triggers. Rebuild with
-- `scriobh search rebuild`.

CREATE TABLE entity_text (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  entity_id     TEXT NOT NULL UNIQUE REFERENCES entities(id) ON DELETE CASCADE,
  compendium_id TEXT NOT NULL,
  name          TEXT NOT NULL DEFAULT '',
  aliases       TEXT NOT NULL DEFAULT '',
  summary       TEXT NOT NULL DEFAULT '',
  body          TEXT NOT NULL DEFAULT ''
);

CREATE INDEX entity_text_compendium ON entity_text(compendium_id);

CREATE VIRTUAL TABLE entity_fts USING fts5(
  name, aliases, summary, body,
  content = 'entity_text', content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER entity_text_ai AFTER INSERT ON entity_text BEGIN
  INSERT INTO entity_fts(rowid, name, aliases, summary, body) VALUES (new.id, new.name, new.aliases, new.summary, new.body);
END;

CREATE TRIGGER entity_text_ad AFTER DELETE ON entity_text BEGIN
  INSERT INTO entity_fts(entity_fts, rowid, name, aliases, summary, body) VALUES ('delete', old.id, old.name, old.aliases, old.summary, old.body);
END;

CREATE TRIGGER entity_text_au AFTER UPDATE ON entity_text BEGIN
  INSERT INTO entity_fts(entity_fts, rowid, name, aliases, summary, body) VALUES ('delete', old.id, old.name, old.aliases, old.summary, old.body);
  INSERT INTO entity_fts(rowid, name, aliases, summary, body) VALUES (new.id, new.name, new.aliases, new.summary, new.body);
END;
