-- 1120 compile: per-project presets, and the author's manuscript contact block.
--
-- A preset is a named set of compile options saved with a project. The built-in
-- presets (US Standard Manuscript, UK manuscript, plain Markdown, styled DOCX,
-- EPUB 3) are code, not rows; a custom preset stores the options it was made
-- with in full, so a later change to a built-in never alters what it produces.
--
-- The contact block is what a manuscript title page shows top left. It is
-- personal data and is written into the file and nowhere else: not into an
-- export's manifest, the audit log, an event, the mirror, a Git push or a
-- stored idempotent response.

CREATE TABLE compile_presets (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  name_key     TEXT NOT NULL,          -- folded name, unique per project
  base         TEXT NOT NULL DEFAULT '', -- the built-in preset it started from
  options_json TEXT NOT NULL,
  created_by   TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

CREATE UNIQUE INDEX compile_presets_name ON compile_presets(project_id, name_key);

CREATE TABLE manuscript_contact (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  name       TEXT NOT NULL DEFAULT '',  -- the legal name for the contact block; empty: the byline
  surname    TEXT NOT NULL DEFAULT '',  -- for the running header; empty: the last word of the byline
  address    TEXT NOT NULL DEFAULT '',  -- lines separated by newlines
  email      TEXT NOT NULL DEFAULT '',
  phone      TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
