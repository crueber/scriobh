-- 1837 whether a Git remote carries the project's own cover (docs/COVERS.md): the cover layout's pictures and the
-- layout itself. Turning it on for a public remote lets more reach a repository people can read, so it is a
-- browser-only, typed-confirmation change like the others (F-861), and a public remote has it off. A private remote (a
-- backup) carries the whole copy, cover included, so its remotes have it on. A remote that already exists keeps what it
-- carried: the cover is new, so no public remote has it.
ALTER TABLE git_remotes ADD COLUMN include_cover INTEGER NOT NULL DEFAULT 0 CHECK (include_cover IN (0,1));
UPDATE git_remotes SET include_cover = 1 WHERE audience = 'private';
