-- 1510 events about a Compendium. The mirror follows compendium_id to the
-- Compendium's own mirror and to every project whose projection depends on it.

ALTER TABLE events ADD COLUMN compendium_id TEXT;
CREATE INDEX events_compendium ON events(compendium_id, seq) WHERE compendium_id IS NOT NULL;
