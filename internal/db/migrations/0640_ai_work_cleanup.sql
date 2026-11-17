-- 0640 What the AI recipes kept goes with the person's access to it.
--
-- Reviews, marks, kept chats, rolling jobs and held suggestions are private to
-- the person who made them and are text from the manuscript. When that person
-- stops being a member of the project, or is no longer an editor (the only
-- roles that can run AI are owner and editor), their rows go in the same
-- transaction as the change of membership, whichever op or administrator made
-- it. (A person's access to a Compendium ending is 1530.)

CREATE TRIGGER ai_work_member_removed AFTER DELETE ON project_members
BEGIN
  DELETE FROM ai_pending WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_reviews WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_marks   WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_chats   WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_jobs    WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
END;

CREATE TRIGGER ai_work_member_demoted AFTER UPDATE OF role ON project_members
WHEN OLD.role IN ('owner','editor') AND NEW.role NOT IN ('owner','editor')
BEGIN
  DELETE FROM ai_pending WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_reviews WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_marks   WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_chats   WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
  DELETE FROM ai_jobs    WHERE user_id = OLD.user_id AND project_id = OLD.project_id;
END;

-- A note is a sentence, an example and a quote; the parser holds these limits,
-- and the database holds them too, so that nothing that reaches the table can
-- make a review a place to keep prose.
CREATE TRIGGER ai_notes_size BEFORE INSERT ON ai_notes
WHEN length(NEW.body) > 2400 OR length(NEW.example) > 640 OR length(NEW.quote) > 1200 OR length(NEW.sheet_json) > 16000
BEGIN
  SELECT RAISE(ABORT, 'a note is limited to a sentence or two, one short example and a short quote');
END;
