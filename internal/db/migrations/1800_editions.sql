-- 1800 editions: where an imported book came from, and the book itself.
--
-- project_sources (1300) says that a project was imported and from what;
-- an edition is the book as it was received: the source file kept intact
-- (files/projects/<project id>/editions/<sha256>.<ext>, never an attachment, so
-- it is not linked from the text and never reaches the mirror or a Git push),
-- its SHA-256 and size, the label a person gave it, the identifier the book
-- carries, and, per chapter, the Markdown the converter made before the
-- writer's edits (compressed) and the path of the chapter in the source. A later
-- diff, against the project's documents or against another edition, reads these
-- and nothing else.
--
-- An edition belongs to its project: it is removed with it (a purge), kept with
-- it in the trash, and travels with a backup because both the rows and the
-- files tree do.

CREATE TABLE editions (
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_id     TEXT REFERENCES project_sources(id) ON DELETE SET NULL,   -- the import record, when there is one
  kind          TEXT NOT NULL CHECK (kind IN ('epub','docx','pdf','markdown')),
  label         TEXT NOT NULL DEFAULT '',            -- what a person calls it: "First edition (2019)"
  identifier    TEXT NOT NULL DEFAULT '',            -- the book's own identifier (an ISBN, a urn:uuid)
  sha256        TEXT NOT NULL,                       -- of the source file's bytes
  size          INTEGER NOT NULL DEFAULT 0,          -- bytes of the source file
  has_file      INTEGER NOT NULL DEFAULT 0 CHECK (has_file IN (0,1)),
  metadata_json TEXT NOT NULL DEFAULT '{}',          -- title, authors, language, publisher, date, counts
  imported_by   TEXT,                                -- the person; counted in their storage
  imported_at   TEXT NOT NULL
);

CREATE INDEX editions_project ON editions(project_id, imported_at, id);
CREATE INDEX editions_sha ON editions(sha256);

-- The book's chapters as read, in reading order. document_id is the document the
-- chapter became (NULL when it was left out, or the document is gone); several
-- chapters can name one document (a merge), and a chapter split into scenes
-- names the chapter's document. parent_ord is the chapter's parent (0: none).
CREATE TABLE edition_chapters (
  edition_id  TEXT NOT NULL REFERENCES editions(id) ON DELETE CASCADE,
  ord         INTEGER NOT NULL,                      -- 1-based, reading order
  parent_ord  INTEGER NOT NULL DEFAULT 0,
  document_id TEXT REFERENCES documents(id) ON DELETE SET NULL,
  source_path TEXT NOT NULL DEFAULT '',              -- the file in the book, with #fragment when a file holds several chapters
  kind        TEXT NOT NULL DEFAULT 'chapter',
  title       TEXT NOT NULL DEFAULT '',
  words       INTEGER NOT NULL DEFAULT 0,
  sha256      TEXT NOT NULL DEFAULT '',              -- of the baseline Markdown
  baseline_gz BLOB,                                  -- the Markdown the converter made, deflated
  PRIMARY KEY (edition_id, ord)
);

CREATE INDEX edition_chapters_doc ON edition_chapters(document_id) WHERE document_id IS NOT NULL;
CREATE INDEX edition_chapters_path ON edition_chapters(edition_id, source_path);

-- The source file counts against the same quotas as an attachment: the project's,
-- the person's and the server's (file_usage, 0800). The same bytes held by two
-- editions of one project are one file and are counted once.
CREATE TRIGGER editions_usage_ins AFTER INSERT ON editions
WHEN NEW.has_file = 1 AND NEW.size > 0
  AND NOT EXISTS (SELECT 1 FROM editions e WHERE e.project_id = NEW.project_id AND e.sha256 = NEW.sha256 AND e.has_file = 1 AND e.id <> NEW.id)
BEGIN
  INSERT INTO file_usage(scope, bytes, files) VALUES ('instance', NEW.size, 1)
    ON CONFLICT(scope) DO UPDATE SET bytes = bytes + NEW.size, files = files + 1;
  INSERT INTO file_usage(scope, bytes, files) VALUES ('owner:' || NEW.project_id, NEW.size, 1)
    ON CONFLICT(scope) DO UPDATE SET bytes = bytes + NEW.size, files = files + 1;
  INSERT INTO file_usage(scope, bytes, files) SELECT 'user:' || NEW.imported_by, NEW.size, 1 WHERE NEW.imported_by IS NOT NULL
    ON CONFLICT(scope) DO UPDATE SET bytes = bytes + NEW.size, files = files + 1;
END;

CREATE TRIGGER editions_usage_del AFTER DELETE ON editions
WHEN OLD.has_file = 1 AND OLD.size > 0
  AND NOT EXISTS (SELECT 1 FROM editions e WHERE e.project_id = OLD.project_id AND e.sha256 = OLD.sha256 AND e.has_file = 1 AND e.id <> OLD.id)
BEGIN
  UPDATE file_usage SET bytes = bytes - OLD.size, files = files - 1 WHERE scope = 'instance';
  UPDATE file_usage SET bytes = bytes - OLD.size, files = files - 1 WHERE scope = 'owner:' || OLD.project_id;
  UPDATE file_usage SET bytes = bytes - OLD.size, files = files - 1 WHERE scope = 'user:' || OLD.imported_by;
  DELETE FROM file_usage WHERE files <= 0 AND scope IN ('owner:' || OLD.project_id, 'user:' || OLD.imported_by);
END;
