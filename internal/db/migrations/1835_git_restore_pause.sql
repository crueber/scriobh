-- 1835 A restore pauses every remote, and the pause is its own fact, not a message that another step can overwrite:
-- restore_paused stays set until the owner switches the remote on (git.remote.update enabled). The start-up
-- credential check never clears it and never switches a paused remote on. A remote is also off for other reasons
-- (credentials the key cannot read); the two coexist.
ALTER TABLE git_remotes ADD COLUMN restore_paused INTEGER NOT NULL DEFAULT 0;
