-- 1520 mention triggers. The stamp bookkeeping of 0520 depends on tables the
-- 1500 migration creates (references, links, overrides), so its triggers live
-- after it. See 0520 for what the stamp means.

-- The vocabulary changes in many places (entity ops, overrides, links,
-- references, a project's primary Compendium, a Compendium's trash and delete,
-- a merge, a bulk loader). Triggers are the one seam none of them can miss: each
-- marks the projects that can see the change as owing a rescan. The statements
-- are the same everywhere: the projects of a Compendium are its primaries, the
-- projects that reference it, and the projects that link one of its entities.

CREATE TRIGGER mentions_entity_ins AFTER INSERT ON entities WHEN NEW.compendium_id IS NOT NULL
BEGIN
  INSERT INTO mention_state(project_id, stamp)
    SELECT pid, lower(hex(randomblob(8))) FROM (
      SELECT id AS pid FROM projects WHERE compendium_id = NEW.compendium_id AND deleted_at IS NULL
      UNION SELECT owner_id FROM compendium_refs WHERE owner_kind = 'project' AND target_compendium_id = NEW.compendium_id
      UNION SELECT l.project_id FROM project_entity_links l WHERE l.entity_id = NEW.id) s
    JOIN projects p ON p.id = s.pid WHERE true
    ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

-- name as well as name_key: the capital guard reads the name as written, so
-- "elara" renamed "Elara" changes what matches though the folded key does not.
CREATE TRIGGER mentions_entity_upd AFTER UPDATE OF name, name_key, trashed_at ON entities
WHEN NEW.compendium_id IS NOT NULL AND (OLD.name <> NEW.name OR OLD.name_key <> NEW.name_key OR OLD.trashed_at IS NOT NEW.trashed_at)
BEGIN
  INSERT INTO mention_state(project_id, stamp)
    SELECT pid, lower(hex(randomblob(8))) FROM (
      SELECT id AS pid FROM projects WHERE compendium_id = NEW.compendium_id AND deleted_at IS NULL
      UNION SELECT owner_id FROM compendium_refs WHERE owner_kind = 'project' AND target_compendium_id = NEW.compendium_id
      UNION SELECT l.project_id FROM project_entity_links l WHERE l.entity_id = NEW.id) s
    JOIN projects p ON p.id = s.pid WHERE true
    ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_entity_del AFTER DELETE ON entities WHEN OLD.compendium_id IS NOT NULL
BEGIN
  INSERT INTO mention_state(project_id, stamp)
    SELECT pid, lower(hex(randomblob(8))) FROM (
      SELECT id AS pid FROM projects WHERE compendium_id = OLD.compendium_id AND deleted_at IS NULL
      UNION SELECT owner_id FROM compendium_refs WHERE owner_kind = 'project' AND target_compendium_id = OLD.compendium_id
      UNION SELECT l.project_id FROM project_entity_links l WHERE l.entity_id = OLD.id) s
    JOIN projects p ON p.id = s.pid WHERE true
    ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_alias_ins AFTER INSERT ON entity_aliases
BEGIN
  INSERT INTO mention_state(project_id, stamp)
    SELECT pid, lower(hex(randomblob(8))) FROM (
      SELECT p.id AS pid FROM entities e JOIN projects p ON p.compendium_id = e.compendium_id AND p.deleted_at IS NULL WHERE e.id = NEW.entity_id
      UNION SELECT r.owner_id FROM entities e JOIN compendium_refs r ON r.owner_kind = 'project' AND r.target_compendium_id = e.compendium_id WHERE e.id = NEW.entity_id
      UNION SELECT l.project_id FROM project_entity_links l WHERE l.entity_id = NEW.entity_id) s
    JOIN projects p ON p.id = s.pid WHERE true
    ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_alias_upd AFTER UPDATE OF alias_key, entity_id ON entity_aliases
WHEN OLD.alias_key <> NEW.alias_key OR OLD.entity_id <> NEW.entity_id
BEGIN
  INSERT INTO mention_state(project_id, stamp)
    SELECT pid, lower(hex(randomblob(8))) FROM (
      SELECT p.id AS pid FROM entities e JOIN projects p ON p.compendium_id = e.compendium_id AND p.deleted_at IS NULL WHERE e.id IN (OLD.entity_id, NEW.entity_id)
      UNION SELECT r.owner_id FROM entities e JOIN compendium_refs r ON r.owner_kind = 'project' AND r.target_compendium_id = e.compendium_id WHERE e.id IN (OLD.entity_id, NEW.entity_id)
      UNION SELECT l.project_id FROM project_entity_links l WHERE l.entity_id IN (OLD.entity_id, NEW.entity_id)) s
    JOIN projects p ON p.id = s.pid WHERE true
    ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_alias_del AFTER DELETE ON entity_aliases
BEGIN
  INSERT INTO mention_state(project_id, stamp)
    SELECT pid, lower(hex(randomblob(8))) FROM (
      SELECT p.id AS pid FROM entities e JOIN projects p ON p.compendium_id = e.compendium_id AND p.deleted_at IS NULL WHERE e.id = OLD.entity_id
      UNION SELECT r.owner_id FROM entities e JOIN compendium_refs r ON r.owner_kind = 'project' AND r.target_compendium_id = e.compendium_id WHERE e.id = OLD.entity_id
      UNION SELECT l.project_id FROM project_entity_links l WHERE l.entity_id = OLD.entity_id) s
    JOIN projects p ON p.id = s.pid WHERE true
    ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_override_ins AFTER INSERT ON project_entity_overrides
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT NEW.project_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_override_upd AFTER UPDATE OF name, entity_id ON project_entity_overrides
WHEN OLD.name IS NOT NEW.name OR OLD.entity_id <> NEW.entity_id
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT NEW.project_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_override_del AFTER DELETE ON project_entity_overrides
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT OLD.project_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_ref_ins AFTER INSERT ON compendium_refs WHEN NEW.owner_kind = 'project'
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT NEW.owner_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = NEW.owner_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_ref_del AFTER DELETE ON compendium_refs WHEN OLD.owner_kind = 'project'
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT OLD.owner_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = OLD.owner_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_link_ins AFTER INSERT ON project_entity_links
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT NEW.project_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_link_upd AFTER UPDATE OF entity_id ON project_entity_links
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT NEW.project_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_link_del AFTER DELETE ON project_entity_links
BEGIN
  INSERT INTO mention_state(project_id, stamp) SELECT OLD.project_id, lower(hex(randomblob(8))) WHERE EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_primary AFTER UPDATE OF compendium_id ON projects
WHEN OLD.compendium_id IS NOT NEW.compendium_id
BEGIN
  INSERT INTO mention_state(project_id, stamp) VALUES (NEW.id, lower(hex(randomblob(8)))) ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;

CREATE TRIGGER mentions_compendium_state AFTER UPDATE OF trashed_at, deleted_at ON compendiums
WHEN OLD.trashed_at IS NOT NEW.trashed_at OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
  INSERT INTO mention_state(project_id, stamp)
    SELECT pid, lower(hex(randomblob(8))) FROM (
      SELECT id AS pid FROM projects WHERE compendium_id = NEW.id AND deleted_at IS NULL
      UNION SELECT owner_id FROM compendium_refs WHERE owner_kind = 'project' AND target_compendium_id = NEW.id
      UNION SELECT l.project_id FROM project_entity_links l JOIN entities e ON e.id = l.entity_id WHERE e.compendium_id = NEW.id) s
    JOIN projects p ON p.id = s.pid WHERE true
    ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp;
END;
