-- 1836 The repositories a project has ever given a public remote, by normalised address (scheme, host, port, path).
-- A remote made or changed to carry the full mirror at one of them needs a person at a browser, and a push of such a
-- remote is refused while the host says the repository is public. The row outlives the remote: deleting a public
-- remote and making a private one at the same address does not clear the mark.
CREATE TABLE git_public_addresses (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  address    TEXT NOT NULL,
  since      TEXT NOT NULL,
  PRIMARY KEY (project_id, address)
);
