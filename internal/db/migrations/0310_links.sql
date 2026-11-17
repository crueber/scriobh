-- 0310 wiki links. One row per distinct link in a document's body.
--
-- to_id is NULL while the link is unresolved (no live document in the project
-- has that title). Because NULLs never collide in a UNIQUE index, the key is
-- (from_id, kind, link_text): to_id is a function of the project and the link
-- text, so this is the same key as DATA-MODEL's (from_id, to_id, kind,
-- link_text) with unresolved links covered too.
--
-- target_key is the folded form of link_text (Unicode lower case, whitespace
-- collapsed) computed in Go. SQLite's lower() is ASCII-only, so matching is
-- done on this key, never in SQL.

CREATE TABLE document_links (
  id          TEXT PRIMARY KEY,
  from_id     TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  to_id       TEXT REFERENCES documents(id) ON DELETE SET NULL,
  kind        TEXT NOT NULL DEFAULT 'wiki' CHECK (kind IN ('wiki','mention','embed')),
  link_text   TEXT NOT NULL,
  target_key  TEXT NOT NULL,
  alias       TEXT NOT NULL DEFAULT '',    -- the shown text of [[Title|text]], first occurrence
  ord         INTEGER NOT NULL DEFAULT 0,  -- order of first occurrence in the body
  occurrences INTEGER NOT NULL DEFAULT 1,
  ambiguous   INTEGER NOT NULL DEFAULT 0,  -- more than one live document has this title
  snippet     TEXT NOT NULL DEFAULT '',    -- plain text around the first occurrence
  created_at  TEXT NOT NULL,
  UNIQUE (from_id, kind, link_text)
);

CREATE INDEX document_links_to  ON document_links(to_id, kind) WHERE to_id IS NOT NULL;
CREATE INDEX document_links_key ON document_links(target_key);
