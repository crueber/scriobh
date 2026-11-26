-- 1322 import provenance: the state a document was left in, not only its text.
--
-- 1321 recorded the hash of the text an import stored. That is not enough to know a document was left alone: a move, a reorder, an entity
-- link, an exclusion from the AI, a comment, a suggestion, a tag, a timeline link, a share link, a site flag or a chapter marked done change
-- the document or what hangs from it without a save, and so without a new revision. state_hash is a hash of every column of the document
-- that a person can change (all but the stamps, the revision, the trash marks and the counts derived from the text), and attach_hash of what
-- is attached to it by other tables (the explicit list is docstate.go; a test fails when a table that refers to a document is not on it),
-- both taken at the end of the import transaction. import.undo takes a document only when both are what they were. A row from before this
-- migration has them empty, and is kept.

ALTER TABLE document_sources ADD COLUMN state_hash  TEXT NOT NULL DEFAULT '';
ALTER TABLE document_sources ADD COLUMN attach_hash TEXT NOT NULL DEFAULT '';
