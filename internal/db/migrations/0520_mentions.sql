-- 0520 mentions: which entities each document mentions, and the bookkeeping
-- that keeps the index honest when the vocabulary changes.
--
-- entity_mentions holds one row per (document, entity): how many times the
-- entity's name or an alias occurs in the document's prose, how many of those
-- were explicit [[wiki links]], and a snippet around the first. It is
-- aggregate by design: the places of each occurrence (for highlighting) are
-- computed from the Markdown when asked (mention.list), so the index stays a
-- few rows per document however long the text is. A row names the entity, never
-- its text; who may read the entity decides who sees the row (the ops filter).
--
-- The index is derived. Deleting the rows is always safe: `scriobh mentions
-- rebuild` (or the start-up repair) brings them back from doc_text.

CREATE TABLE entity_mentions (
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  entity_id   TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  count       INTEGER NOT NULL,                -- occurrences of the name or an alias in the prose; 0 for a document that only links the entity
  names       INTEGER NOT NULL DEFAULT 0,      -- of count, found as the entity's name
  aliases     INTEGER NOT NULL DEFAULT 0,      -- of count, found as an alias
  explicit    INTEGER NOT NULL DEFAULT 0,      -- occurrences of [[links]] that name the entity (a link that shows other words is counted only here)
  ambiguous   INTEGER NOT NULL DEFAULT 0,      -- another entity in the project's scope answers to the same word
  first_pos   INTEGER NOT NULL DEFAULT 0,      -- rune offset of the first occurrence in the search text
  snippet     TEXT NOT NULL DEFAULT '',        -- plain text around the first occurrence
  PRIMARY KEY (document_id, entity_id)
);

CREATE INDEX entity_mentions_entity ON entity_mentions(entity_id, project_id);
CREATE INDEX entity_mentions_project ON entity_mentions(project_id);

-- mention_state says whether a project's index was built against its current
-- vocabulary. stamp changes (to a new random value, in the same transaction as
-- the change) whenever anything the vocabulary is made of changes: an entity,
-- an alias, an override, the project's Compendiums. indexed_stamp is the stamp
-- the last complete scan was built against. They differ while a rescan is
-- owed. The stamp is an opaque token compared for equality: it is not a clock
-- and not an id, and a rolled-back change can never collide with a committed
-- one, which a counter could.
CREATE TABLE mention_state (
  project_id    TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  stamp         TEXT NOT NULL,
  indexed_stamp TEXT NOT NULL DEFAULT '',
  indexed_at    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX mention_state_owed ON mention_state(project_id) WHERE stamp <> indexed_stamp;
