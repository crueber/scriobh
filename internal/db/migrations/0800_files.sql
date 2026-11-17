-- 0800 files: attachments. A blob on disk is named by its SHA-256 and kept once
-- per owner (a project or a Compendium): files/projects/<id>/attachments/<sha256>.<ext>.
-- The row is what people and the API see; the type is detected from the bytes,
-- never taken from the client.

CREATE TABLE attachments (
  id            TEXT PRIMARY KEY,
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,
  compendium_id TEXT REFERENCES compendiums(id) ON DELETE CASCADE,
  sha256        TEXT NOT NULL,
  bytes         INTEGER NOT NULL,
  mime          TEXT NOT NULL,                 -- detected, from a short allow-list
  ext           TEXT NOT NULL,                 -- the extension of the stored file, from the detected type
  kind          TEXT NOT NULL CHECK (kind IN ('image','pdf','text')),
  filename      TEXT NOT NULL,                 -- sanitised display name, never used as a path
  alt           TEXT NOT NULL DEFAULT '',      -- alt text for an image, a description for anything else
  width         INTEGER NOT NULL DEFAULT 0,
  height        INTEGER NOT NULL DEFAULT 0,
  created_by    TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  CHECK ((project_id IS NULL) <> (compendium_id IS NULL))
);

-- Deduplicated per owner: the same bytes uploaded twice to one project are one attachment.
CREATE UNIQUE INDEX attachments_project_sha ON attachments(project_id, sha256) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX attachments_compendium_sha ON attachments(compendium_id, sha256) WHERE compendium_id IS NOT NULL;
CREATE INDEX attachments_project_created ON attachments(project_id, created_at, id) WHERE project_id IS NOT NULL;
CREATE INDEX attachments_compendium_created ON attachments(compendium_id, created_at, id) WHERE compendium_id IS NOT NULL;
CREATE INDEX attachments_user ON attachments(created_by);

-- Where an attachment is used: Markdown links of the form ![alt](/files/att_...)
-- found in a document or a research note when it was saved. ref_id is a document
-- or research note id; it is polymorphic, so the owner's cascade removes the rows
-- through the attachment they point at.
CREATE TABLE attachment_refs (
  attachment_id TEXT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
  ref_kind      TEXT NOT NULL CHECK (ref_kind IN ('doc','research')),
  ref_id        TEXT NOT NULL,
  PRIMARY KEY (attachment_id, ref_kind, ref_id)
);

CREATE INDEX attachment_refs_ref ON attachment_refs(ref_kind, ref_id);

-- Storage in use, kept by triggers so a purge's cascade is counted too. The
-- scopes are 'instance', 'user:<id>' (what a person uploaded) and
-- 'owner:<project or compendium id>'. Upload checks these inside its own
-- transaction, so two uploads cannot both squeeze under a quota.
CREATE TABLE file_usage (
  scope TEXT PRIMARY KEY,
  bytes INTEGER NOT NULL DEFAULT 0,
  files INTEGER NOT NULL DEFAULT 0
);

CREATE TRIGGER attachments_usage_ins AFTER INSERT ON attachments BEGIN
  INSERT INTO file_usage(scope, bytes, files) VALUES ('instance', NEW.bytes, 1)
    ON CONFLICT(scope) DO UPDATE SET bytes = bytes + NEW.bytes, files = files + 1;
  INSERT INTO file_usage(scope, bytes, files) VALUES ('owner:' || COALESCE(NEW.project_id, NEW.compendium_id), NEW.bytes, 1)
    ON CONFLICT(scope) DO UPDATE SET bytes = bytes + NEW.bytes, files = files + 1;
  INSERT INTO file_usage(scope, bytes, files) SELECT 'user:' || NEW.created_by, NEW.bytes, 1 WHERE NEW.created_by IS NOT NULL
    ON CONFLICT(scope) DO UPDATE SET bytes = bytes + NEW.bytes, files = files + 1;
END;

CREATE TRIGGER attachments_usage_del AFTER DELETE ON attachments BEGIN
  UPDATE file_usage SET bytes = bytes - OLD.bytes, files = files - 1 WHERE scope = 'instance';
  UPDATE file_usage SET bytes = bytes - OLD.bytes, files = files - 1 WHERE scope = 'owner:' || COALESCE(OLD.project_id, OLD.compendium_id);
  UPDATE file_usage SET bytes = bytes - OLD.bytes, files = files - 1 WHERE scope = 'user:' || OLD.created_by;
  DELETE FROM file_usage WHERE files <= 0 AND scope IN ('owner:' || COALESCE(OLD.project_id, OLD.compendium_id), 'user:' || OLD.created_by);
END;
