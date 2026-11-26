-- 0480 comment visibility: a comment is shared (everyone who can read the project's comments, as every comment
-- was until now) or private, "Only me": the author alone. Not the owner, not other editors, not an instance
-- administrator who is not the author.
--
-- visibility is set when the comment is written. A private comment can become shared later (a later op); a
-- shared one never becomes private again, because people may already have been told of it. shared_at is when a
-- comment became visible to others: the time the author's 15 minutes to reword it run from.
--
-- A reply has its thread's visibility. The op enforces that with a message that names the rule; the trigger is
-- the second line of defence, so a bug cannot put a shared reply under a private thread.

ALTER TABLE comments ADD COLUMN visibility TEXT NOT NULL DEFAULT 'shared' CHECK (visibility IN ('shared','private'));
ALTER TABLE comments ADD COLUMN shared_at TEXT;
UPDATE comments SET shared_at = created_at;
CREATE INDEX comments_private ON comments(project_id, author_id, document_id) WHERE visibility = 'private';

CREATE TRIGGER comments_reply_visibility BEFORE INSERT ON comments WHEN NEW.parent_id IS NOT NULL
BEGIN
  SELECT RAISE(ABORT, 'a reply takes the visibility of its thread')
  WHERE NEW.visibility <> (SELECT visibility FROM comments WHERE id = NEW.parent_id);
END;
