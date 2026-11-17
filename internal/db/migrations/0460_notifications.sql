-- 0460 in-app notifications: a mention, a reply to my comment, my thread resolved, a
-- comment on my project, a suggestion for me to decide, the decision on mine. A row names ids
-- only (the comment, the suggestion, the document and who did it); the text is
-- read from the comment when the list is shown, so a deleted or edited comment
-- never leaves its words here. Nothing is sent anywhere: no email, no push.
-- actor_id is NULL for a guest of a share link.

CREATE TABLE notifications (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('mention','reply','comment','resolved','suggestion','suggestion-decided')),
  comment_id    TEXT REFERENCES comments(id) ON DELETE CASCADE,
  suggestion_id TEXT REFERENCES suggestions(id) ON DELETE CASCADE,
  document_id   TEXT,
  actor_id      TEXT,
  created_at    TEXT NOT NULL,
  read_at       TEXT
);

CREATE INDEX notifications_user ON notifications(user_id, read_at, created_at);
CREATE INDEX notifications_project ON notifications(project_id);
