-- 0370 the mention worker pages through a project's documents by id
-- (WHERE project_id = ? AND id > ? ORDER BY id LIMIT n). documents_tree covers
-- (project_id, parent_id, position), which cannot serve that range, so a large
-- project was sorted on every page.

CREATE INDEX documents_project_id ON documents(project_id, id);
