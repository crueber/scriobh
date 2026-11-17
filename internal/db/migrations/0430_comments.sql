-- 0430 review comments: threads anchored to a range of a document, to the
-- document as a whole, or to the project. Comments are NOT document content:
-- they are not in documents.content, never in an export, the mirror, a Git push,
-- the search text, a word count or an AI context, and a project purge removes
-- them with the rest.
--
-- A thread is a root (parent_id NULL) and its replies (parent_id names the root;
-- replies are one level deep). Only a root has a status and an anchor.
--
-- The anchor is a range of the document's body in UTF-16 code units at
-- anchor_revision, with the quote, the text it holds now, anchor_original, the
-- text it held when the comment was made (never rewritten), and up to 24
-- characters of context either side, so it can be found again after the text
-- changes (alts.Rebase, run in the save transaction). anchor_state is 'orphaned'
-- when the text is gone: the range is then not trusted and the comment is
-- listed, never moved by a guess.
--
-- The author is a person (author_id), a guest of a share link (share_id and
-- guest_name, author_id NULL) or a person since removed (both empty). share_id
-- has no foreign key: share_links is created later, and the project cascade
-- removes the rows with it.
--
-- A deleted comment keeps its row so a thread keeps its shape: body is emptied,
-- deleted_at is set and the anchor is dropped.

CREATE TABLE comments (
  id              TEXT PRIMARY KEY,
  project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  document_id     TEXT REFERENCES documents(id) ON DELETE CASCADE,
  parent_id       TEXT REFERENCES comments(id) ON DELETE CASCADE,
  kind            TEXT NOT NULL DEFAULT 'comment' CHECK (kind IN ('comment','question','suggestion-note')),
  author_id       TEXT REFERENCES users(id) ON DELETE SET NULL,
  share_id        TEXT,
  guest_name      TEXT NOT NULL DEFAULT '',
  body            TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  resolved_by     TEXT REFERENCES users(id) ON DELETE SET NULL,
  resolved_at     TEXT,
  anchor_state    TEXT NOT NULL DEFAULT 'none' CHECK (anchor_state IN ('none','active','orphaned')),
  anchor_start    INTEGER NOT NULL DEFAULT 0,
  anchor_end      INTEGER NOT NULL DEFAULT 0,
  anchor_quote    TEXT NOT NULL DEFAULT '',
  anchor_original TEXT NOT NULL DEFAULT '',
  anchor_prefix   TEXT NOT NULL DEFAULT '',
  anchor_suffix   TEXT NOT NULL DEFAULT '',
  anchor_hash     TEXT NOT NULL DEFAULT '',
  anchor_revision INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  edited_at       TEXT,
  deleted_at      TEXT
);

CREATE INDEX comments_doc     ON comments(document_id, anchor_state, anchor_start) WHERE parent_id IS NULL;
CREATE INDEX comments_project ON comments(project_id, status, created_at) WHERE parent_id IS NULL;
CREATE INDEX comments_parent  ON comments(parent_id, created_at) WHERE parent_id IS NOT NULL;
CREATE INDEX comments_author  ON comments(project_id, author_id);
CREATE INDEX comments_share   ON comments(share_id) WHERE share_id IS NOT NULL;

-- Who a comment mentions: members of the project, resolved when it was written.
CREATE TABLE comment_mentions (
  comment_id TEXT NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (comment_id, user_id)
) WITHOUT ROWID;

CREATE INDEX comment_mentions_user ON comment_mentions(user_id);
