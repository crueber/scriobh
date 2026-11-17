-- 0450 share links: read-only and comment-only access for a person with no
-- account. Only the SHA-256 of the secret half of the token is stored (the token
-- is shown once, when the link is made). A link names the documents it shows
-- (none: the whole manuscript), and may carry a passphrase (argon2id, never the
-- passphrase), an expiry and a download flag. Counters and last_used_at are
-- written in batches (internal/domain/share), not per request.

CREATE TABLE share_links (
  id              TEXT PRIMARY KEY,
  project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  label           TEXT NOT NULL DEFAULT '',
  scope           TEXT NOT NULL CHECK (scope IN ('read','comment')),
  token_hash      TEXT NOT NULL,
  passphrase_hash TEXT,
  allow_download  INTEGER NOT NULL DEFAULT 0 CHECK (allow_download IN (0,1)),
  ghosts          TEXT NOT NULL DEFAULT 'omit' CHECK (ghosts IN ('omit','keep')),
  expires_at      TEXT,
  views           INTEGER NOT NULL DEFAULT 0,
  comment_count   INTEGER NOT NULL DEFAULT 0,
  failed_unlocks  INTEGER NOT NULL DEFAULT 0,
  locked_until    TEXT,
  created_by      TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  revoked_at      TEXT,
  last_used_at    TEXT
);

CREATE INDEX share_links_project ON share_links(project_id, created_at);

-- The documents a link shows, each with its descendants. Empty: the manuscript.
CREATE TABLE share_link_docs (
  share_id    TEXT NOT NULL REFERENCES share_links(id) ON DELETE CASCADE,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  PRIMARY KEY (share_id, document_id)
) WITHOUT ROWID;

CREATE INDEX share_link_docs_doc ON share_link_docs(document_id);
