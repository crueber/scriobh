-- 0360 scratchpads: one private scratch document per person per project, and one
-- per person per Compendium. It is never shared: no op reads another person's
-- row (an administrator's reads return the administrator's own), it is not
-- searched, exported, mirrored, pushed to Git or sent in an event, and it goes
-- when the person, the project or the Compendium does. It is in the database
-- (so a backup holds it, and a person with the database file can read it).

CREATE TABLE scratchpads (
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,
  compendium_id TEXT REFERENCES compendiums(id) ON DELETE CASCADE,
  content       TEXT NOT NULL DEFAULT '',
  revision      INTEGER NOT NULL DEFAULT 1,
  updated_at    TEXT NOT NULL,
  CHECK ((project_id IS NULL) <> (compendium_id IS NULL))
);

CREATE UNIQUE INDEX scratchpads_project ON scratchpads(user_id, project_id) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX scratchpads_compendium ON scratchpads(user_id, compendium_id) WHERE compendium_id IS NOT NULL;
