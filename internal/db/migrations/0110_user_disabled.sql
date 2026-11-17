-- 0110 a disabled account: an administrator can switch a person off without removing them.
--
-- A disabled user cannot sign in, has no live session and no working API key, and nothing they wrote is touched
-- (their projects, comments and history stay, and the projects stay theirs). Unlike deleted_at, which is a removal
-- and refuses the identity for good, disabled_at is undone by an administrator. Both are checked wherever a person
-- is let in: the sign-in, the session lookup, the API-key lookup (and so a live stream, which looks both up again).
ALTER TABLE users ADD COLUMN disabled_at TEXT;
ALTER TABLE users ADD COLUMN disabled_by TEXT;
