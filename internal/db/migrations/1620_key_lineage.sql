-- 1620 key lineage: which key made a key. Revoking a key revokes the keys it
-- made, and a child never outlives its parent.
ALTER TABLE api_keys ADD COLUMN parent_key_id TEXT REFERENCES api_keys(id) ON DELETE SET NULL;

CREATE INDEX api_keys_parent ON api_keys(parent_key_id) WHERE parent_key_id IS NOT NULL;
