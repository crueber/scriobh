-- 1400 alternatives and ghosts.
--
-- An alternative is an annotation on a range of a document's body: the body
-- always holds the chosen wording, and the variants live here. These tables are
-- the canonical store; the trailing `<!-- scriobh:alts ... -->` line of
-- documents.content is their serialisation, written by the server in the same
-- transaction (internal/domain/altstore). Offsets are UTF-16 code units over
-- the body (the text before that line) with LF line endings.
--
-- chosen_variant_id has no foreign key: it names a row of the child table, which
-- cannot exist before its parent, and the save path keeps the two consistent.
-- The original variant is immutable: a trigger refuses to change its text or its
-- flag, and a unique index allows one per alternative. Deleting an alternative
-- (resolving it) deletes its variants; the text it replaced stays in the
-- revision that was recorded before.

CREATE TABLE alternatives (
  id                TEXT PRIMARY KEY,
  document_id       TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  project_id        TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  scope             TEXT NOT NULL CHECK (scope IN ('word','sentence','paragraph')),
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','orphaned')),
  anchor_start      INTEGER NOT NULL DEFAULT 0,
  anchor_end        INTEGER NOT NULL DEFAULT 0,
  anchor_prefix     TEXT NOT NULL DEFAULT '',   -- up to 24 characters of Markdown before the anchor
  anchor_suffix     TEXT NOT NULL DEFAULT '',   -- and after it
  anchor_hash       TEXT NOT NULL DEFAULT '',   -- sha256 of the chosen text, hex
  chosen_variant_id TEXT,
  created_by        TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);

CREATE INDEX alternatives_doc     ON alternatives(document_id, anchor_start);
CREATE INDEX alternatives_project ON alternatives(project_id, status);

CREATE TABLE alternative_variants (
  id             TEXT PRIMARY KEY,
  alternative_id TEXT NOT NULL REFERENCES alternatives(id) ON DELETE CASCADE,
  text           TEXT NOT NULL,                  -- Markdown source
  source         TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','generated')),
  is_original    INTEGER NOT NULL DEFAULT 0 CHECK (is_original IN (0,1)),
  position       REAL NOT NULL,
  author_id      TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at     TEXT NOT NULL
);

CREATE INDEX alternative_variants_alt ON alternative_variants(alternative_id, position);
CREATE UNIQUE INDEX alternative_variants_original ON alternative_variants(alternative_id) WHERE is_original = 1;

CREATE TRIGGER alternative_variants_original_immutable
BEFORE UPDATE OF text, is_original ON alternative_variants
WHEN old.is_original = 1 AND (new.text <> old.text OR new.is_original <> 1)
BEGIN
  SELECT RAISE(ABORT, 'the original wording of an alternative is immutable');
END;

-- ghosts: an index of the ghosted ranges of a document, rebuilt from the
-- Markdown on every save (the paired <scriobh-ghost> tags are the truth). It
-- exists to give a ghost an id that survives saves, to say who ghosted it and
-- when, and to list ghosts without parsing. Offsets are UTF-16 units of the
-- ghosted text, between the tags.
CREATE TABLE ghosts (
  id           TEXT PRIMARY KEY,
  document_id  TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  anchor_start INTEGER NOT NULL,
  anchor_end   INTEGER NOT NULL,
  text_hash    TEXT NOT NULL,                    -- sha256 of the ghosted text, hex
  preview      TEXT NOT NULL DEFAULT '',         -- the first 160 characters
  word_count   INTEGER NOT NULL DEFAULT 0,       -- the words the count leaves out
  created_by   TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at   TEXT NOT NULL
);

CREATE INDEX ghosts_doc ON ghosts(document_id, anchor_start);
CREATE INDEX ghosts_project ON ghosts(project_id);
