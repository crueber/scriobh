-- 0330 content stamp: when the body last changed and who changed it. updated_at
-- also moves on a rename, a move or a trash, so it cannot say when the text an
-- autosave revision captures became the head. Rows written by code that does
-- not set these (an import, an older version) read as '' and NULL; revision
-- capture then falls back to updated_at and no author.

ALTER TABLE documents ADD COLUMN content_at TEXT NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN content_by TEXT;
