-- 1321 import provenance: the hash of what was stored.
--
-- document_sources.source_hash (1300) is the hash of the file as it was read. The text a document
-- holds is not always those bytes (line endings are normalised, a chapter cut from a Word file or
-- joined from two has no file of its own, alternatives are reconciled), so it cannot say whether a
-- document has been changed since it was imported. stored_hash is the hash of the content the
-- import wrote (documents.content_hash at that moment). import.undo trashes a document only when
-- its content still has that hash and it has not been saved since. Rows made before this migration
-- have it empty; for those the undo compares against source_hash, and anything it cannot prove is
-- unchanged is kept.

ALTER TABLE document_sources ADD COLUMN stored_hash TEXT NOT NULL DEFAULT '';
