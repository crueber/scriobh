-- 1851 compendium changes (F-478).
--
-- Merging two Compendiums, or splitting one, moves entities between them in one transaction. The same transaction
-- writes one row here: the plan that was applied and the before-state, in ids and the text that was merged away or
-- dropped (a merged entity's whole sheet, a dropped relationship), so a move that was not wanted can be put back from
-- what is recorded. It is a record, read by the owner through compendium.change.list and get, and it goes with
-- either Compendium when that is purged: text must not outlive the Compendium it belonged to.
CREATE TABLE compendium_changes (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('merge','split')),
  source_id    TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  target_id    TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  actor_id     TEXT,
  created_at   TEXT NOT NULL,
  fingerprint  TEXT NOT NULL,
  summary_json TEXT NOT NULL,
  before_json  TEXT NOT NULL
);

CREATE INDEX compendium_changes_source ON compendium_changes(source_id, created_at);
CREATE INDEX compendium_changes_target ON compendium_changes(target_id, created_at);
