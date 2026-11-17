-- 0400 revisions: append-only history of a document's body.
--
-- Content lives in revision_blobs, once per (document, content hash), and a
-- revision row references it. Identical text stored again (a manual snapshot of
-- an unchanged autosave, a restore of an old version) costs a small row, not a
-- second copy. Blobs are per document, not global, so they vanish with the
-- document and a purged project leaves nothing behind. Content above 8 KB is
-- gzip-compressed (encoding 'gzip'); smaller content is stored as is.

CREATE TABLE revision_blobs (
  document_id  TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  content_hash TEXT NOT NULL,                 -- sha256 hex of the raw Markdown, equal to documents.content_hash
  encoding     TEXT NOT NULL CHECK (encoding IN ('identity','gzip')),
  content      BLOB NOT NULL,
  raw_size     INTEGER NOT NULL,
  PRIMARY KEY (document_id, content_hash)
) WITHOUT ROWID;

CREATE TABLE document_revisions (
  id           TEXT PRIMARY KEY,
  document_id  TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  revision     INTEGER NOT NULL,              -- documents.revision this content had
  content_hash TEXT NOT NULL,
  word_count   INTEGER NOT NULL DEFAULT 0,
  author_id    TEXT REFERENCES users(id) ON DELETE SET NULL,
  reason       TEXT NOT NULL CHECK (reason IN ('autosave','manual','snapshot','restore','import','ai-accepted','alternative')),
  label        TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,                 -- when this text became the document's head (what people see)
  recorded_at  TEXT NOT NULL,                 -- when this row was written (what the autosave throttle reads)
  FOREIGN KEY (document_id, content_hash) REFERENCES revision_blobs(document_id, content_hash) ON DELETE CASCADE
);

CREATE INDEX document_revisions_shown    ON document_revisions(document_id, created_at, id);
CREATE INDEX document_revisions_recorded ON document_revisions(document_id, recorded_at, id);
