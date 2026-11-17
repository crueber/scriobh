-- 1500 compendiums: a Compendium is a library item that holds the shared
-- material of one story world or subject. It exists with or without a project.
-- Membership is its own: project membership never grants it.

CREATE TABLE compendiums (
  id            TEXT PRIMARY KEY,
  owner_id      TEXT NOT NULL REFERENCES users(id),
  name          TEXT NOT NULL,
  slug          TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'fiction' CHECK (kind IN ('fiction','nonfiction')),
  description   TEXT NOT NULL DEFAULT '',
  settings_json TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT,              -- reserved, as on projects: nothing sets it; purge deletes the row
  trashed_at    TEXT,
  archived_at   TEXT
);

-- Slugs are unique per owner, as for projects.
CREATE UNIQUE INDEX compendiums_slug ON compendiums(owner_id, slug);
CREATE INDEX compendiums_owner ON compendiums(owner_id, deleted_at);

CREATE TABLE compendium_members (
  compendium_id TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role          TEXT NOT NULL CHECK (role IN ('owner','editor','reader')),
  created_at    TEXT NOT NULL,
  PRIMARY KEY (compendium_id, user_id)
);

CREATE INDEX compendium_members_user ON compendium_members(user_id);

-- projects.compendium_id (0200) is the project's single primary Compendium, so
-- "at most one" is the column itself. SQLite cannot add a foreign key to an
-- existing column and rebuilding projects is not worth it, so triggers keep
-- the pointer honest: it must name a Compendium, and it is cleared when that
-- Compendium is deleted.
CREATE TRIGGER projects_compendium_exists_ins BEFORE INSERT ON projects
WHEN NEW.compendium_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM compendiums WHERE id = NEW.compendium_id)
BEGIN SELECT RAISE(ABORT, 'projects.compendium_id must name a compendium'); END;

CREATE TRIGGER projects_compendium_exists_upd BEFORE UPDATE OF compendium_id ON projects
WHEN NEW.compendium_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM compendiums WHERE id = NEW.compendium_id)
BEGIN SELECT RAISE(ABORT, 'projects.compendium_id must name a compendium'); END;

CREATE INDEX projects_compendium ON projects(compendium_id) WHERE compendium_id IS NOT NULL;

-- A project or a Compendium may reference another Compendium's whole content,
-- read-only. owner_id is polymorphic, so triggers stand in for the cascade.
CREATE TABLE compendium_refs (
  id                   TEXT PRIMARY KEY,
  owner_kind           TEXT NOT NULL CHECK (owner_kind IN ('project','compendium')),
  owner_id             TEXT NOT NULL,
  target_compendium_id TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  mode                 TEXT NOT NULL DEFAULT 'reference' CHECK (mode IN ('reference','copy')),
  created_by           TEXT,
  created_at           TEXT NOT NULL,
  UNIQUE (owner_kind, owner_id, target_compendium_id)
);

CREATE INDEX compendium_refs_target ON compendium_refs(target_compendium_id);

CREATE TRIGGER compendiums_detach_projects AFTER DELETE ON compendiums
BEGIN
  UPDATE projects SET compendium_id = NULL WHERE compendium_id = OLD.id;
  DELETE FROM compendium_refs WHERE owner_kind = 'compendium' AND owner_id = OLD.id;
END;

CREATE TRIGGER projects_delete_refs AFTER DELETE ON projects
BEGIN
  DELETE FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = OLD.id;
END;

-- A per-book view of a shared entity. A NULL name or summary inherits the
-- Compendium's; fields_json holds only the keys the book changes.
CREATE TABLE project_entity_overrides (
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  entity_id   TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  name        TEXT,
  summary     TEXT,
  fields_json TEXT NOT NULL DEFAULT '{}',
  notes       TEXT NOT NULL DEFAULT '',
  updated_by  TEXT,
  updated_at  TEXT NOT NULL,
  PRIMARY KEY (project_id, entity_id)
);

CREATE INDEX project_entity_overrides_entity ON project_entity_overrides(entity_id);

-- One entity from a Compendium the project does not otherwise reference (a cameo).
CREATE TABLE project_entity_links (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  entity_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  mode       TEXT NOT NULL DEFAULT 'reference' CHECK (mode IN ('reference','copy')),
  created_by TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, entity_id)
);

CREATE INDEX project_entity_links_entity ON project_entity_links(entity_id);

-- Custom field definitions: per Compendium, per entity kind.
CREATE TABLE entity_field_defs (
  id            TEXT PRIMARY KEY,
  compendium_id TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('character','place','item','org','concept')),
  key           TEXT NOT NULL,
  label         TEXT NOT NULL,
  type          TEXT NOT NULL CHECK (type IN ('text','number','date','choice')),
  choices_json  TEXT NOT NULL DEFAULT '[]',
  position      REAL NOT NULL DEFAULT 65536,
  created_at    TEXT NOT NULL,
  UNIQUE (compendium_id, kind, key)
);

-- Entity references from scenes arrive in a later migration; until then a
-- deleted entity must not leave a dangling point-of-view or location.
CREATE INDEX documents_pov_entity ON documents(pov_entity_id) WHERE pov_entity_id IS NOT NULL;
CREATE INDEX documents_location_entity ON documents(location_entity_id) WHERE location_entity_id IS NOT NULL;

CREATE TRIGGER entities_clear_scene_refs AFTER DELETE ON entities
BEGIN
  UPDATE documents SET pov_entity_id = NULL WHERE pov_entity_id = OLD.id;
  UPDATE documents SET location_entity_id = NULL WHERE location_entity_id = OLD.id;
END;

-- Compendium exports are recorded like project exports, with a manifest.
CREATE TABLE compendium_exports (
  id            TEXT PRIMARY KEY,
  compendium_id TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  user_id       TEXT NOT NULL REFERENCES users(id),
  preset        TEXT NOT NULL DEFAULT '',
  format        TEXT NOT NULL DEFAULT 'md' CHECK (format IN ('md')),
  filename      TEXT NOT NULL,
  bytes         INTEGER NOT NULL DEFAULT 0,
  sha256        TEXT NOT NULL DEFAULT '',
  manifest_json TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL
);

CREATE INDEX compendium_exports_compendium ON compendium_exports(compendium_id, created_at);
