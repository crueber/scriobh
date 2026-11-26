-- 0421 a version can be written before a big cut (reason 'cut'), before a split
-- ('split') and before a join ('join'), so the Versions list can say why it was
-- kept. A CHECK list cannot be changed in place, so the table is rebuilt with the
-- longer list, as 0420 did; the rows and the two indexes are carried over
-- unchanged. Nothing references document_revisions, so the rebuild cascades
-- nowhere. (revision_blobs is the parent and is not touched.)

CREATE TABLE document_revisions_new (
  id           TEXT PRIMARY KEY,
  document_id  TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  revision     INTEGER NOT NULL,
  content_hash TEXT NOT NULL,
  word_count   INTEGER NOT NULL DEFAULT 0,
  author_id    TEXT REFERENCES users(id) ON DELETE SET NULL,
  reason       TEXT NOT NULL CHECK (reason IN ('autosave','manual','snapshot','restore','import','ai-accepted','alternative','suggestion-accepted','cut','split','join')),
  label        TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  recorded_at  TEXT NOT NULL,
  FOREIGN KEY (document_id, content_hash) REFERENCES revision_blobs(document_id, content_hash) ON DELETE CASCADE
);

INSERT INTO document_revisions_new (id, document_id, revision, content_hash, word_count, author_id, reason, label, note, created_at, recorded_at)
  SELECT id, document_id, revision, content_hash, word_count, author_id, reason, label, note, created_at, recorded_at FROM document_revisions;

DROP TABLE document_revisions;
ALTER TABLE document_revisions_new RENAME TO document_revisions;

CREATE INDEX document_revisions_shown    ON document_revisions(document_id, created_at, id);
CREATE INDEX document_revisions_recorded ON document_revisions(document_id, recorded_at, id);
