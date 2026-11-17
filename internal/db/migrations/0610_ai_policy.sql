-- 0610 per-project AI policy. A project with no row, or with mode 'off', sends
-- nothing to any provider. The policy is read and enforced on the server for
-- every AI request; the browser's idea of it is not consulted.

CREATE TABLE ai_policies (
  project_id         TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  mode               TEXT NOT NULL DEFAULT 'off' CHECK (mode IN ('off','ask','local_only')),
  actions_json       TEXT NOT NULL DEFAULT '[]',  -- the actions allowed (selection, chat, critique, ...)
  providers_json     TEXT NOT NULL DEFAULT '[]',  -- provider ids allowed; empty: any the person may use
  models_json        TEXT NOT NULL DEFAULT '{}',  -- per provider id, the models allowed; absent: any
  chains_json        TEXT NOT NULL DEFAULT '{}',  -- per recipe, action or "default": the ordered provider and model to try
  redaction          TEXT NOT NULL DEFAULT 'off' CHECK (redaction IN ('off','entities','names')),
  agents_may_use     INTEGER NOT NULL DEFAULT 0,  -- an API key may run AI on this project only when the owner says so
  budget_cost_micros INTEGER NOT NULL DEFAULT 0,  -- the owner's own monthly cap for this project; 0 none
  budget_tokens      INTEGER NOT NULL DEFAULT 0,
  style_guide        TEXT NOT NULL DEFAULT '',    -- the author's voice rules, sent as a labelled block
  updated_by         TEXT,
  updated_at         TEXT NOT NULL
);

-- A document, research note or entity flagged never-send is left out of every
-- AI request, whoever asks and however it is named. (documents.ai_exclude has
-- existed since 0300.)
ALTER TABLE research_notes ADD COLUMN ai_exclude INTEGER NOT NULL DEFAULT 0;
ALTER TABLE entities ADD COLUMN ai_exclude INTEGER NOT NULL DEFAULT 0;
