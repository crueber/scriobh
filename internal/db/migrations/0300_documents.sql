-- 0300 documents: the manuscript tree. Full DATA-MODEL shape; columns for later
-- phases (entities, compile, AI) exist now so the table is never rebuilt.

CREATE TABLE documents (
  id                  TEXT PRIMARY KEY,
  project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  compendium_id       TEXT,              -- set for Compendium research; FK arrives with the 1500 range
  parent_id           TEXT REFERENCES documents(id) ON DELETE CASCADE,
  kind                TEXT NOT NULL DEFAULT 'chapter'
    CHECK (kind IN ('part','chapter','scene','note','front_matter','back_matter','research')),
  title               TEXT NOT NULL DEFAULT '',
  content             TEXT NOT NULL DEFAULT '',
  content_hash        TEXT NOT NULL DEFAULT '',
  synopsis_short      TEXT NOT NULL DEFAULT '',
  synopsis_long       TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'draft'
    CHECK (status IN ('idea','outline','draft','revise','done')),
  position            REAL NOT NULL DEFAULT 65536,
  revision            INTEGER NOT NULL DEFAULT 1,   -- the client's concurrency token
  word_count          INTEGER NOT NULL DEFAULT 0,
  char_count          INTEGER NOT NULL DEFAULT 0,
  target_words        INTEGER NOT NULL DEFAULT 0,
  pov_entity_id       TEXT,
  location_entity_id  TEXT,
  story_date          TEXT NOT NULL DEFAULT '',
  story_date_end      TEXT NOT NULL DEFAULT '',
  include_in_compile  INTEGER NOT NULL DEFAULT 1,
  ai_exclude          INTEGER NOT NULL DEFAULT 0,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  deleted_at          TEXT,
  trash_batch         TEXT              -- ties a trashed subtree together so restore brings back exactly what one trash took
);

CREATE INDEX documents_tree    ON documents(project_id, parent_id, position);
CREATE INDEX documents_kind    ON documents(project_id, kind);
CREATE INDEX documents_status  ON documents(project_id, status);
CREATE INDEX documents_updated ON documents(project_id, updated_at);
