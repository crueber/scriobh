-- 1140 site: static site publishing for blog mode.
--
-- site_options holds a project's options for its site (title, base address,
-- posts per page, accent, ...) as one JSON document, merged over the defaults
-- when read. site_docs holds what the site needs to know about a document and
-- the manuscript does not: whether it is a post or a page (auto: decided by the
-- project's selection rule), an address of its own, a publication date, an
-- excerpt and tags. They sit beside documents, not in them, so the document
-- write path, the mirror and a Git push never see them. site_state is the last
-- build and the last publish, for the status a screen shows.
--
-- None of this is ever manuscript text except the excerpt, which the owner
-- writes for the site and is published as written.

CREATE TABLE site_options (
  project_id   TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  options_json TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

CREATE TABLE site_docs (
  document_id  TEXT PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  role         TEXT NOT NULL DEFAULT 'auto' CHECK (role IN ('auto','post','page','skip')),
  slug         TEXT NOT NULL DEFAULT '',
  slug_key     TEXT NOT NULL DEFAULT '',   -- folded slug, unique per project when set
  published_at TEXT NOT NULL DEFAULT '',   -- RFC 3339 UTC, or empty
  excerpt      TEXT NOT NULL DEFAULT '',
  tags_json    TEXT NOT NULL DEFAULT '[]',
  updated_at   TEXT NOT NULL
);

CREATE INDEX site_docs_project ON site_docs(project_id);
CREATE UNIQUE INDEX site_docs_slug ON site_docs(project_id, slug_key) WHERE slug_key <> '';

CREATE TABLE site_state (
  project_id          TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  build_at            TEXT NOT NULL DEFAULT '',
  build_site_hash     TEXT NOT NULL DEFAULT '',
  build_input_hash    TEXT NOT NULL DEFAULT '',
  build_posts         INTEGER NOT NULL DEFAULT 0,
  build_files         INTEGER NOT NULL DEFAULT 0,
  build_bytes         INTEGER NOT NULL DEFAULT 0,
  publish_at          TEXT NOT NULL DEFAULT '',
  publish_site_hash   TEXT NOT NULL DEFAULT '',
  publish_input_hash  TEXT NOT NULL DEFAULT '',
  publish_posts       INTEGER NOT NULL DEFAULT 0,
  publish_files       INTEGER NOT NULL DEFAULT 0,
  publish_bytes       INTEGER NOT NULL DEFAULT 0,
  publish_dir         TEXT NOT NULL DEFAULT ''   -- the folder name under the site root, kept once chosen
);
