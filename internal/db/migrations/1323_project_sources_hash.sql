-- 1323 import provenance: the duplicate check of a new project's import.
--
-- A new project's import looks for an earlier import of the same file (the same source_hash) into any project the caller can read. The
-- index that exists, project_sources_project, is by project_id, so that search read every row of every project. This index is by the hash
-- and then the import time, so the search reads only the imports of that file. Nothing else reads it.

CREATE INDEX project_sources_hash ON project_sources(source_hash, imported_at);
