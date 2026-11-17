-- 1830 Phase 7: what a remote may carry, and the settings that hang off a remote.
--
--   audience      private: the full mirror (manuscript, notes, alternatives, outline details), a backup
--                 public:  only what a reader of the book sees (the strictest filter; see gitmirror/audience.go)
--                 Rows that exist before this migration keep the behaviour they had: private. A remote made
--                 from now on is public unless its owner says otherwise.
--   only_done     with audience public: only documents whose status is done
--   sign_commits  sign the commits of this remote with the project's signing key (1834)
--   created_via   '' for a remote added by hand, 'publish' for one made by repo.publish
-- No CHECK on the new columns: a CHECK is frozen once applied, and the code validates both values.
ALTER TABLE git_remotes ADD COLUMN audience     TEXT    NOT NULL DEFAULT 'private';
ALTER TABLE git_remotes ADD COLUMN only_done    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE git_remotes ADD COLUMN sign_commits INTEGER NOT NULL DEFAULT 0;
ALTER TABLE git_remotes ADD COLUMN created_via  TEXT    NOT NULL DEFAULT '';
