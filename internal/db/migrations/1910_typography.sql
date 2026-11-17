-- 1910 custom typography (F-085).
--
-- How a project's rich (writing) and reading views are set: a typeface from the ones already shipped or the system's,
-- a size relative to the reader's own, a line height, a measure, a paragraph style and a scene-break style. Only
-- values from a fixed list are stored (internal/domain/typography validates them); there is no CSS, URL or font here.
-- It is never applied to exports, compile, print or the static site.
--
-- A project with no row is set exactly as before. Each person may keep an override that wins in their own browser.
-- Two new tables; nothing existing changes.

CREATE TABLE project_typography (
  project_id    TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  settings_json TEXT NOT NULL,
  updated_by    TEXT,
  updated_at    TEXT NOT NULL
);

CREATE TABLE user_typography (
  user_id       TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  settings_json TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
