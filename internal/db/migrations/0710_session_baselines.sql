-- 0710 session baselines: keep other people's words out of a writer's credit,
-- and keep the baselines (which are copies of manuscript text) from outliving
-- the access that made them.
--
-- last_hash is the hash of the text the session itself last left in the
-- document. When the next save finds a different text underneath it, someone
-- else (or a restore) changed the document in between, and the session banks
-- what it has earned and measures from the text as it now stands.
--
-- The table is rebuilt (it is days old, and SQLite cannot add a foreign key in
-- place) so that a purged document takes its baselines with it. touched_at lets
-- a save prune baselines whose session has long ended, and a trigger removes a
-- person's baselines when they leave a project.

CREATE TABLE writing_session_docs_new (
  session_id     TEXT NOT NULL REFERENCES writing_sessions(id) ON DELETE CASCADE,
  document_id    TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  baseline_text  TEXT NOT NULL,
  baseline_words INTEGER NOT NULL DEFAULT 0,
  carried        INTEGER NOT NULL DEFAULT 0,
  credit         INTEGER NOT NULL DEFAULT 0,
  last_hash      TEXT NOT NULL DEFAULT '',
  touched_at     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (session_id, document_id)
);

-- Existing rows have no last_hash, so the first save after this migration
-- rebases them: conservative, and it loses no credit already banked.
INSERT INTO writing_session_docs_new(session_id, document_id, baseline_text, baseline_words, carried, credit)
  SELECT session_id, document_id, baseline_text, baseline_words, carried, credit
  FROM writing_session_docs WHERE document_id IN (SELECT id FROM documents);

DROP TABLE writing_session_docs;
ALTER TABLE writing_session_docs_new RENAME TO writing_session_docs;

CREATE INDEX writing_session_docs_touched ON writing_session_docs(touched_at);

CREATE TRIGGER writing_session_docs_member_left AFTER DELETE ON project_members
BEGIN
  DELETE FROM writing_session_docs WHERE session_id IN
    (SELECT id FROM writing_sessions WHERE user_id = OLD.user_id AND project_id = OLD.project_id);
END;
