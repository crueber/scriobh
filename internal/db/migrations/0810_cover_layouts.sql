-- 0810 cover layouts (docs/COVERS.md). A project may have one layout: the writer's own cover, which takes
-- the place of the generated one wherever a cover is shown. The pictures are ordinary project attachments;
-- this table holds how they are placed, and attachment_refs holds a 'cover' reference for each one used.
--
-- One row per project at most. The side and wrap documents are JSON that the cover ops validate; the hash
-- is the sha256 of the layout and of the pictures' own hashes, and is part of every export's input hash
-- and every cache key of a derived picture. IDs and times come from Go.

CREATE TABLE cover_layouts (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  shape      TEXT NOT NULL CHECK (shape IN ('sides','wrap')),
  trim       TEXT NOT NULL DEFAULT '6x9',                -- the page size the cover is made for; it fixes the aspect of every side
  front_json TEXT NOT NULL DEFAULT '{"mode":"generated"}',  -- sides only
  back_json  TEXT NOT NULL DEFAULT '{"mode":"none"}',       -- sides only
  wrap_json  TEXT NOT NULL DEFAULT '{}',                    -- wrap only
  alt        TEXT NOT NULL DEFAULT '',                   -- the cover described for screen readers; required when a picture is used
  revision   INTEGER NOT NULL DEFAULT 1,                 -- the concurrency token
  hash       TEXT NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- A cover picture is cleaned (imagesafe.Clean) by the cover upload, and only a picture the cover upload made
-- may be named in a layout: this flag is what says so. An attachment stored by the ordinary upload has it 0.
ALTER TABLE attachments ADD COLUMN cover_clean INTEGER NOT NULL DEFAULT 0 CHECK (cover_clean IN (0,1));

-- attachment_refs gains the 'cover' kind. A CHECK list cannot be changed in place, so the table is rebuilt with
-- the longer list; the rows and the index are carried over unchanged. Nothing references attachment_refs.
CREATE TABLE attachment_refs_new (
  attachment_id TEXT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
  ref_kind      TEXT NOT NULL CHECK (ref_kind IN ('doc','research','cover')),
  ref_id        TEXT NOT NULL,
  PRIMARY KEY (attachment_id, ref_kind, ref_id)
);

INSERT INTO attachment_refs_new (attachment_id, ref_kind, ref_id)
  SELECT attachment_id, ref_kind, ref_id FROM attachment_refs;

DROP TABLE attachment_refs;
ALTER TABLE attachment_refs_new RENAME TO attachment_refs;

CREATE INDEX attachment_refs_ref ON attachment_refs(ref_kind, ref_id);
