-- 0320 scene metadata as free text. pov_entity_id and location_entity_id stay
-- NULL until Phase 3 introduces entities; the text columns are what the outline
-- shows and edits now, and Phase 3 migrates them to entities (OPEN-QUESTIONS).

ALTER TABLE documents ADD COLUMN pov      TEXT NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN location TEXT NOT NULL DEFAULT '';
