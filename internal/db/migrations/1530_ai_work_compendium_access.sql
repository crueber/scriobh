-- 1530 A continuity audit and an extraction hold what a person could read of a
-- Compendium (entity sheets, events, names). When their access to that
-- Compendium ends, so does their copy of what it produced: the person's
-- continuity and extraction reviews on the projects that use the Compendium go
-- in the same transaction as the membership change.

CREATE TRIGGER ai_work_compendium_access_removed AFTER DELETE ON compendium_members
BEGIN
  DELETE FROM ai_reviews WHERE user_id = OLD.user_id AND kind IN ('continuity','extraction') AND project_id IN (
    SELECT id FROM projects WHERE compendium_id = OLD.compendium_id
    UNION
    SELECT owner_id FROM compendium_refs WHERE owner_kind = 'project' AND target_compendium_id = OLD.compendium_id);
END;
