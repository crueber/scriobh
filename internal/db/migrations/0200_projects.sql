-- 0200 projects: projects, members, preferences.

CREATE TABLE projects (
  id                   TEXT PRIMARY KEY,
  owner_id             TEXT NOT NULL REFERENCES users(id),
  compendium_id        TEXT,             -- primary Compendium; FK arrives with the 1500 range
  title                TEXT NOT NULL,
  slug                 TEXT NOT NULL,
  kind                 TEXT NOT NULL DEFAULT 'blank'
    CHECK (kind IN ('novel','novella','short','collection','nonfiction','essay','blog','report','blank')),
  subtitle             TEXT NOT NULL DEFAULT '',
  author_name          TEXT NOT NULL DEFAULT '',
  genre                TEXT NOT NULL DEFAULT '',
  language             TEXT NOT NULL DEFAULT 'en',
  status               TEXT NOT NULL DEFAULT 'idea'
    CHECK (status IN ('idea','outlining','drafting','revising','finished','published')),
  description          TEXT NOT NULL DEFAULT '',
  word_goal            INTEGER NOT NULL DEFAULT 0,
  deadline             TEXT,
  cover_color          TEXT NOT NULL DEFAULT '#2F5D50',
  cover_path           TEXT,
  settings_json        TEXT NOT NULL DEFAULT '{}',
  view_passphrase_hash TEXT,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  deleted_at           TEXT,
  trashed_at           TEXT
);

-- Slugs are unique per owner, so choosing a title never reveals that someone
-- else has a project with that title. Page routes resolve a slug among the
-- projects the caller can open.
CREATE UNIQUE INDEX projects_slug ON projects(owner_id, slug);
CREATE INDEX projects_owner ON projects(owner_id, deleted_at);

CREATE TABLE project_members (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL CHECK (role IN ('owner','editor','reviewer','reader')),
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, user_id)
);

CREATE INDEX project_members_user ON project_members(user_id);

CREATE TABLE user_prefs (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL,
  PRIMARY KEY (user_id, key)
);
