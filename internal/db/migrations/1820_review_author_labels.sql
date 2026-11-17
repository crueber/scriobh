-- 1820 suggestion author labels: a suggestion that came from the tracked
-- changes of a Word file is by a person who is not an account. The Word
-- author's name is kept as a label beside the row: author_id stays NULL, so
-- the row is never read as a person of this installation. A label is text
-- only: no permission follows from it, and it is never matched to a user.
-- (A comment from a Word file keeps its author's name in comments.guest_name
-- with share_id and author_id both NULL, which no share link or account makes.)

ALTER TABLE suggestions ADD COLUMN author_label TEXT NOT NULL DEFAULT '';
