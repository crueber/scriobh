-- 0530 timeline: one timeline per Compendium, many books reading it.
--
-- An event happened in the story world; a scene tells it (zero or more scenes,
-- in any project that uses the Compendium). Two orders exist and neither is
-- stored on the other: story time (the dates, sortable through sort_start and
-- sort_end, which are computed in Go from story_date and story_date_end) and
-- narrative order (the order a book tells things in: derived from the scenes'
-- reading order, or set by hand per book in timeline_narrative).
--
-- The Compendium foreign keys point forward to the 1500 migration, which
-- SQLite allows: a foreign key is resolved when a row is written.

CREATE TABLE timeline_events (
  id                 TEXT PRIMARY KEY,
  compendium_id      TEXT NOT NULL REFERENCES compendiums(id) ON DELETE CASCADE,
  title              TEXT NOT NULL,
  description        TEXT NOT NULL DEFAULT '',          -- Markdown
  story_date         TEXT NOT NULL DEFAULT '',          -- canonical YYYY, YYYY-MM, YYYY-MM-DD or YYYY-MM-DDTHH:MM; '' is undated
  story_date_end     TEXT NOT NULL DEFAULT '',          -- the same, or '' for an event that is a moment
  story_label        TEXT NOT NULL DEFAULT '',          -- free text shown with the date ("Midsummer, Year 312"); never used to order
  sort_start         TEXT NOT NULL DEFAULT '',          -- fixed-width key of the start; '' when undated
  sort_end           TEXT NOT NULL DEFAULT '',          -- fixed-width key of the end of the interval
  location_entity_id TEXT REFERENCES entities(id) ON DELETE SET NULL,
  revision           INTEGER NOT NULL DEFAULT 1,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  trashed_at         TEXT
);

CREATE INDEX timeline_events_order ON timeline_events(compendium_id, sort_start, id);
CREATE INDEX timeline_events_location ON timeline_events(location_entity_id) WHERE location_entity_id IS NOT NULL;

-- Who took part, with a role label ("host", "witness"). An entity of the
-- event's own Compendium; deleting the entity removes the row.
CREATE TABLE timeline_event_people (
  event_id   TEXT NOT NULL REFERENCES timeline_events(id) ON DELETE CASCADE,
  entity_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY (event_id, entity_id)
);

CREATE INDEX timeline_event_people_entity ON timeline_event_people(entity_id);

-- The scenes that tell an event. project_id is the document's project, kept so
-- a project's view is one indexed read; it cascades with the document.
CREATE TABLE timeline_event_docs (
  event_id    TEXT NOT NULL REFERENCES timeline_events(id) ON DELETE CASCADE,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  created_at  TEXT NOT NULL,
  PRIMARY KEY (event_id, document_id)
);

CREATE INDEX timeline_event_docs_project ON timeline_event_docs(project_id, document_id);
CREATE INDEX timeline_event_docs_doc ON timeline_event_docs(document_id);

-- A book's own ordering of events, when it tells them in an order that neither
-- the dates nor the scenes give. A project with no rows here uses the derived
-- order. position is a REAL gap like every other order in Scríobh.
CREATE TABLE timeline_narrative (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  event_id   TEXT NOT NULL REFERENCES timeline_events(id) ON DELETE CASCADE,
  position   REAL NOT NULL,
  PRIMARY KEY (project_id, event_id)
);

CREATE INDEX timeline_narrative_event ON timeline_narrative(event_id);
