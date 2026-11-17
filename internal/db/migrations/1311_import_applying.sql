-- 1311 import jobs: a flag for "a person is applying this job right now".
--
-- The status list of import_jobs (1310) is closed, and 'running' means a clone
-- in progress: recovery deletes the clone of a running job. Applying a ready
-- job needs its own mark, set by a compare-and-set so that two requests for one
-- job cannot create two projects.

ALTER TABLE import_jobs ADD COLUMN applying INTEGER NOT NULL DEFAULT 0;
