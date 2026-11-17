-- 1890 semantic search: an optional, per-project index of embeddings.
--
-- Off for every project until its owner has seen what would be sent and where,
-- and confirmed. The index is derived data: the chosen wording of the
-- manuscript, cut into chunks, one vector for each. It holds no text (a
-- snippet is cut from doc_text when a search runs, and checked against the
-- chunk's hash), only offsets, a hash and the vector.
--
-- A vector is stored as int8 with one multiplier per vector: the vector is
-- scaled to unit length, each number is rounded to a multiple of its largest
-- absolute value / 127, and mult is that step divided by the length of what was
-- stored, so the dot product of a unit query with the stored bytes, times mult,
-- is the cosine of the stored vector. It is a quarter of the size of float32
-- (768 bytes for 768 dimensions).
--
-- embed_projects.provider_id has no foreign key on purpose: deleting a provider
-- pauses an index (the worker says why), it must not delete the owner's choice.

CREATE TABLE embed_projects (
  project_id      TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  enabled         INTEGER NOT NULL DEFAULT 0,
  provider_id     TEXT NOT NULL DEFAULT '',
  model           TEXT NOT NULL DEFAULT '',
  endpoint_key    TEXT NOT NULL DEFAULT '',   -- provider, normalised address, local flag and model, as confirmed
  allow_remote    INTEGER NOT NULL DEFAULT 0, -- the owner typed the confirmation for a remote endpoint
  enabled_by      TEXT NOT NULL DEFAULT '',   -- whose budget and whose personal provider the index uses
  consent_at      TEXT NOT NULL DEFAULT '',
  consent_chunks  INTEGER NOT NULL DEFAULT 0, -- what the preview said at the time
  consent_bytes   INTEGER NOT NULL DEFAULT 0,
  index_version   INTEGER NOT NULL DEFAULT 0,
  dim             INTEGER NOT NULL DEFAULT 0, -- 0 until the first vector arrives
  gen             INTEGER NOT NULL DEFAULT 0, -- bumped by every change to the chunks; keys the in-memory copy
  paused_reason   TEXT NOT NULL DEFAULT '',   -- '' running; restore, endpoint_changed, policy_off, provider_unusable, ...
  last_error_class TEXT NOT NULL DEFAULT '',
  last_error_at   TEXT NOT NULL DEFAULT '',
  fail_count      INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL DEFAULT '',   -- back-off after a failure
  indexed_at      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);

-- One row for each document the worker has dealt with: the document hash it
-- embedded, and under which endpoint and rules. A document with no text, or one
-- the endpoint refused, has a row and no chunks, so it is not tried again until
-- its text changes.
CREATE TABLE embed_docs (
  document_id   TEXT PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  content_hash  TEXT NOT NULL,
  model_key     TEXT NOT NULL,
  index_version INTEGER NOT NULL,
  chunks        INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL CHECK (status IN ('ok','empty','failed','truncated')),
  error_class   TEXT NOT NULL DEFAULT '',
  embedded_at   TEXT NOT NULL
);

CREATE INDEX embed_docs_project ON embed_docs(project_id);

CREATE TABLE embed_chunks (
  id          INTEGER PRIMARY KEY,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ord         INTEGER NOT NULL,
  start_pos   INTEGER NOT NULL,           -- characters into doc_text.body, as search.query counts them
  end_pos     INTEGER NOT NULL,
  words       INTEGER NOT NULL,
  text_hash   TEXT NOT NULL,              -- sha256 of the chunk's text, to prove a snippet is what was embedded
  model       TEXT NOT NULL,
  dim         INTEGER NOT NULL,
  vec         BLOB NOT NULL,              -- dim signed bytes
  mult        REAL NOT NULL,
  UNIQUE (document_id, ord)
);

CREATE INDEX embed_chunks_project ON embed_chunks(project_id, document_id, ord);
