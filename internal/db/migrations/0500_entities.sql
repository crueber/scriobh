-- 0500 entities: the entity bible. Entities belong to a Compendium (or, later,
-- to one project); aliases, relationships and merge tombstones hang off them.
--
-- The foreign keys to compendiums point forward to a table the 1500 migration
-- creates. SQLite resolves a foreign key when a row is written, not when the
-- table is declared, and every migration has run before the first write.

CREATE TABLE entities (
  id            TEXT PRIMARY KEY,
  compendium_id TEXT REFERENCES compendiums(id) ON DELETE CASCADE,
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE, -- a project-local entity; no op writes one yet
  kind          TEXT NOT NULL CHECK (kind IN ('character','place','item','org','concept')),
  name          TEXT NOT NULL,
  name_key      TEXT NOT NULL,                -- folded in Go (text.FoldTitle): SQLite's lower() is ASCII-only
  sort_name     TEXT NOT NULL DEFAULT '',
  sort_key      TEXT NOT NULL,                -- folded sort_name, else name: the list order
  summary       TEXT NOT NULL DEFAULT '',     -- one or two lines
  body          TEXT NOT NULL DEFAULT '',     -- Markdown
  fields_json   TEXT NOT NULL DEFAULT '{}',   -- custom field values, key to canonical string
  color         TEXT NOT NULL DEFAULT '',     -- '' or #rrggbb
  revision      INTEGER NOT NULL DEFAULT 1,   -- optimistic concurrency token
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  trashed_at    TEXT,
  CHECK ((compendium_id IS NULL) <> (project_id IS NULL))
);

-- A name is unique among a Compendium's live entities, case and spacing folded.
CREATE UNIQUE INDEX entities_name ON entities(compendium_id, name_key) WHERE compendium_id IS NOT NULL AND trashed_at IS NULL;
CREATE INDEX entities_order ON entities(compendium_id, sort_key, id);
CREATE INDEX entities_kind ON entities(compendium_id, kind, sort_key);
CREATE INDEX entities_project ON entities(project_id) WHERE project_id IS NOT NULL;

CREATE TABLE entity_aliases (
  id         TEXT PRIMARY KEY,
  entity_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  alias      TEXT NOT NULL,
  alias_key  TEXT NOT NULL,
  notes      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE (entity_id, alias_key)
);

CREATE INDEX entity_aliases_key ON entity_aliases(alias_key);

-- A typed edge from one entity to another in the same Compendium. label reads
-- from the source ("mother of"); inverse_label reads from the target
-- ("daughter of"); a symmetric edge reads the same both ways.
CREATE TABLE entity_relations (
  id              TEXT PRIMARY KEY,
  compendium_id   TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  from_entity_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  to_entity_id    TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  kind            TEXT NOT NULL DEFAULT 'custom'
    CHECK (kind IN ('family','ally','rival','mentor','romantic','member','custom')),
  label           TEXT NOT NULL DEFAULT '',
  inverse_label   TEXT NOT NULL DEFAULT '',
  symmetric       INTEGER NOT NULL DEFAULT 0,
  notes           TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  CHECK (from_entity_id <> to_entity_id),
  UNIQUE (from_entity_id, to_entity_id, kind, label)
);

CREATE INDEX entity_relations_to ON entity_relations(to_entity_id);
CREATE INDEX entity_relations_compendium ON entity_relations(compendium_id);

-- A merge deletes the source entity and leaves this row, so an id someone kept
-- still resolves (to the target) for a while. snapshot_json keeps the source's
-- text as it was; nothing in it is ever shown unless a person asks.
CREATE TABLE entity_redirects (
  from_id       TEXT PRIMARY KEY,
  to_id         TEXT NOT NULL,
  compendium_id TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  from_name     TEXT NOT NULL DEFAULT '',
  merged_by     TEXT,
  summary_json  TEXT NOT NULL DEFAULT '{}',  -- counts of what moved
  snapshot_json TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL
);

CREATE INDEX entity_redirects_to ON entity_redirects(to_id);
CREATE INDEX entity_redirects_compendium ON entity_redirects(compendium_id);
