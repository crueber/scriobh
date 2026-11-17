-- 1200 Git sync: push-only remotes and the history of pushes.
--
-- A remote belongs to one project. Credentials are sealed with the instance key
-- (internal/secrets) and never leave the row except into a transport call; no
-- op returns secret_cipher. The local repository that commits are built in
-- lives under data/git/<project id>/<remote id>/ and is not in the database.

CREATE TABLE git_remotes (
  id               TEXT PRIMARY KEY,
  project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  preset           TEXT NOT NULL DEFAULT '',          -- forgejo, github or empty: how the form was filled, never behaviour
  url              TEXT NOT NULL,                     -- ssh:// or https://, never with credentials
  branch           TEXT NOT NULL DEFAULT 'main',
  path_prefix      TEXT NOT NULL DEFAULT '',          -- optional subdirectory of the repository
  auth_kind        TEXT NOT NULL DEFAULT 'ssh_deploy_key' CHECK (auth_kind IN ('ssh_deploy_key','https_token')),
  username         TEXT NOT NULL DEFAULT '',          -- the user name that goes with an HTTPS token (not secret)
  secret_cipher    TEXT NOT NULL DEFAULT '',          -- the private key or the token, sealed; '' until set
  public_key       TEXT NOT NULL DEFAULT '',          -- authorized_keys line of the generated deploy key
  known_hosts      TEXT NOT NULL DEFAULT '',          -- the pinned host key: "host:port keytype base64"
  push_mode        TEXT NOT NULL DEFAULT 'on_save' CHECK (push_mode IN ('on_save','interval','manual')),
  interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 5 AND 10080),
  include_notes    INTEGER NOT NULL DEFAULT 1,
  author_name      TEXT NOT NULL DEFAULT '',          -- '' means the default identity
  author_email     TEXT NOT NULL DEFAULT '',
  enabled          INTEGER NOT NULL DEFAULT 1,

  -- Status. state says what needs attention:
  --   new       nothing pushed yet
  --   synced    the remote has the newest commit
  --   behind    a commit is waiting locally (the remote was unreachable or a push failed); retried with backoff
  --   rejected  the remote has commits that are not ours; nothing is pushed until the writer acts
  --   blocked   a person must act: bad credentials, host key not accepted or changed, a secret in the tree
  -- (no CHECK on state or on git_pushes.trigger: a CHECK is frozen once applied, and the code validates both)
  state            TEXT NOT NULL DEFAULT 'new',
  last_push_at     TEXT,
  last_attempt_at  TEXT,
  last_commit_sha  TEXT NOT NULL DEFAULT '',          -- the newest commit the remote is known to have
  local_sha        TEXT NOT NULL DEFAULT '',          -- the newest commit in the local repository
  remote_sha       TEXT NOT NULL DEFAULT '',          -- the remote's head as last observed
  mirror_hash      TEXT NOT NULL DEFAULT '',          -- the mirror tree the local head was built from
  last_error       TEXT NOT NULL DEFAULT '',
  last_error_code  TEXT NOT NULL DEFAULT '',
  fail_count       INTEGER NOT NULL DEFAULT 0,
  next_attempt_at  TEXT,
  created_by       TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  UNIQUE (project_id, name)
);

CREATE INDEX git_remotes_project ON git_remotes(project_id);

CREATE TABLE git_pushes (
  id            TEXT PRIMARY KEY,
  remote_id     TEXT NOT NULL REFERENCES git_remotes(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  trigger       TEXT NOT NULL,                        -- save, interval, manual, retry, force
  status        TEXT NOT NULL CHECK (status IN ('running','ok','rejected','error','cancelled')),
  outcome       TEXT NOT NULL DEFAULT '',             -- pushed, up_to_date, remote_ahead, unreachable, auth, host_key, secret, ...
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  started_by    TEXT REFERENCES users(id) ON DELETE SET NULL,
  commit_sha    TEXT NOT NULL DEFAULT '',
  parent_sha    TEXT NOT NULL DEFAULT '',
  remote_sha    TEXT NOT NULL DEFAULT '',             -- the remote's head before this push
  branch        TEXT NOT NULL DEFAULT '',
  files_changed INTEGER NOT NULL DEFAULT 0,
  bytes         INTEGER NOT NULL DEFAULT 0,
  forced        INTEGER NOT NULL DEFAULT 0,
  error_code    TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  manifest_json TEXT NOT NULL DEFAULT '{}',           -- path -> sha256 of what the commit holds
  detail_json   TEXT NOT NULL DEFAULT '{}'            -- the remote's commits that are not ours; the bundle written before a force push
);

CREATE INDEX git_pushes_remote ON git_pushes(remote_id, started_at);
CREATE INDEX git_pushes_project ON git_pushes(project_id, started_at);

-- At most one push in flight per remote, enforced by the database.
CREATE UNIQUE INDEX git_pushes_one_running ON git_pushes(remote_id) WHERE status = 'running';
