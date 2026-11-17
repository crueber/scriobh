-- 1831 Connected accounts at a code host (GitHub, Forgejo or Gitea): a person's own access token, used to list
-- the repositories it may write to and to create one. The token is sealed with the instance key (internal/secrets),
-- bound to the row and to the API address it was entered for, and is never returned, logged or exported. It
-- belongs to the person who entered it: no one else, an administrator included, can use it. A remote made from it
-- keeps a sealed copy of its own, so disconnecting the account does not break a remote.
CREATE TABLE git_accounts (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL,              -- github or forgejo (Gitea speaks the same API)
  host          TEXT NOT NULL,              -- the web host: github.com, or the Forgejo host with an optional port
  login         TEXT NOT NULL,              -- the account the token belongs to, as the host reported it
  label         TEXT NOT NULL DEFAULT '',
  token_sealed  TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  last_used_at  TEXT,
  UNIQUE (user_id, provider, host, login)
);

CREATE INDEX git_accounts_user ON git_accounts(user_id);
