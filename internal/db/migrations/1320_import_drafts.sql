-- 1320 import drafts.
--
-- A file chosen for import is held on the server, in data/imports/stage/<id>/, while the writer reviews
-- what would be created, and this row holds the review itself: the options the file is read with, the
-- writer's edits (renames, kinds, joins, moves, what is left out, the order) and a revision that makes
-- two tabs safe. The row holds no text of the book, only ids, counts, names the writer typed and a hash.
-- The staged file is deleted when the import is applied, when the draft is discarded and after the idle
-- limit; a finished draft keeps only its counts and the new project's id, for a week, so a reload of the
-- review page can say what happened. A Git import's draft points at its import_jobs row (source_kind
-- 'git_job'): the clone stays owned by the Git service.

CREATE TABLE import_drafts (
  id                TEXT PRIMARY KEY,
  owner_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_kind       TEXT NOT NULL CHECK (source_kind IN ('upload','git_job')),
  source_ref        TEXT NOT NULL DEFAULT '',   -- the Git job's id; empty for an upload (its folder is the draft's id)
  kind              TEXT NOT NULL,              -- docx, epub, pdf, zip, pandoc, file, folder, git
  name              TEXT NOT NULL DEFAULT '',
  target_project_id TEXT REFERENCES projects(id) ON DELETE CASCADE, -- NULL: a new project
  target_parent_id  TEXT NOT NULL DEFAULT '',
  bytes             INTEGER NOT NULL DEFAULT 0, -- what the staged files hold on disk, for the quota
  sha256            TEXT NOT NULL DEFAULT '',
  options_json      TEXT NOT NULL DEFAULT '{}', -- the options the source is read with (docx_headings, split_scenes, epub_sections ...)
  edits_json        TEXT NOT NULL DEFAULT '{}', -- edits by node key, the order lists, what is left out, the project's title, kind and author
  prev_json         TEXT NOT NULL DEFAULT '',   -- options and edits before the last change, for one Undo
  revision          INTEGER NOT NULL DEFAULT 1,
  token             TEXT NOT NULL DEFAULT '',   -- the preview token of the current state
  status            TEXT NOT NULL CHECK (status IN ('open','applying','done')),
  result_json       TEXT NOT NULL DEFAULT '{}', -- after the apply: counts and ids, no text
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);

CREATE INDEX import_drafts_owner ON import_drafts(owner_id, updated_at);
CREATE INDEX import_drafts_target ON import_drafts(target_project_id);
