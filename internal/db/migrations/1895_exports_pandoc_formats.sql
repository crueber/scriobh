-- 1895 exports of the Pandoc bridge: an export is recorded under the format it
-- was written in, and a CHECK list cannot be changed in place, so the table is
-- rebuilt with the longer list (odt, rtf, latex, rst and mediawiki are written by
-- Pandoc when an administrator has switched the bridge on). The rows and the
-- index are carried over unchanged. Nothing references exports, so the rebuild
-- cascades nowhere.

CREATE TABLE exports_new (
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id       TEXT NOT NULL REFERENCES users(id),
  preset        TEXT NOT NULL DEFAULT '',
  format        TEXT NOT NULL CHECK (format IN ('md','html','docx','epub','pdf','odt','rtf','latex','rst','mediawiki')),
  filename      TEXT NOT NULL,
  bytes         INTEGER NOT NULL DEFAULT 0,
  sha256        TEXT NOT NULL DEFAULT '',
  manifest_json TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL
);

INSERT INTO exports_new (id, project_id, user_id, preset, format, filename, bytes, sha256, manifest_json, created_at)
  SELECT id, project_id, user_id, preset, format, filename, bytes, sha256, manifest_json, created_at FROM exports;

DROP TABLE exports;
ALTER TABLE exports_new RENAME TO exports;

CREATE INDEX exports_project ON exports(project_id, created_at);
