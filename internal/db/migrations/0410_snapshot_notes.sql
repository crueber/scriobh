-- 0410 system snapshots explain themselves in the note, not the label. A label
-- pins a revision forever, and the copies a restore or a split keeps are safety
-- nets that retention may thin. Move the text of any already written.

UPDATE document_revisions
   SET note = label, label = ''
 WHERE reason = 'snapshot' AND note = '' AND label IN ('Before restore', 'Before split');
