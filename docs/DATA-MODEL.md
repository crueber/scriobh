# Data model

SQLite, WAL mode, foreign keys on, `busy_timeout` 5 s. One writer. Schema changes only through numbered migrations.

## Conventions

| Rule | Detail |
| --- | --- |
| IDs | `TEXT`, prefixed and sortable: `prj_01J…`, `doc_01J…`, `ent_01J…`. Generated in Go, never by SQLite |
| Times | `TEXT`, RFC3339 UTC, generated in Go. Never rely on SQLite's clock |
| Ordering | `position REAL`, gaps of 65536, midpoint inserts, rebalance below 1 |
| Deletion | Soft delete with `deleted_at`; trash is a query, emptying it is the only hard delete |
| Text | UTF-8. No length limit at the column level; caps are enforced per field in the op |
| JSON | `settings_json`, `fields_json`, `payload_json` are for extensible, non-queried data. Anything filtered or sorted gets a column |
| Money | integer micro-units (`cost_micros`) with a currency column, never floats |
| Derived | `word_count`, `char_count` are denormalised, written in the same transaction, rebuildable by command |

## Migration ranges

Never edit an applied migration. Add the next number in the owning range.

| Range | Owner |
| --- | --- |
| `0001_core` | connection, `schema_migrations`, nothing else. Never edited |
| `0100` | auth: users, sessions, keys, roles |
| `0200` | projects, members, templates, preferences |
| `0300` | documents, tree, tags, links |
| `0400` | revisions (`0400_revisions`, `0410_snapshot_notes`), then review: the revision reason `suggestion-accepted` (`0420`, a table rebuild: a CHECK list cannot be changed in place), comments and mentions (`0430`), suggestions (`0440`), share links (`0450`), notifications (`0460`); later `1820_review_author_labels` (the Word author's name on a suggestion) |
| `0500` | entities, aliases, relations, merge redirects (`0500_entities`); the mention index and its state (`0520_mentions`); the timeline (`0530_timeline`) |
| `0600` | AI: providers and prices (`0600_ai_providers`), per-project policy and the never-send flags (`0610_ai_policy`), runs, usage and budgets (`0620_ai_runs`) |
| `0700` | goals, sessions, daily stats |
| `0800` | files, attachments, covers, quotas |
| `0900` | FTS tables and triggers (`0900_search` for documents, `0910_entity_search` for entities) |
| `1000` | events, audit log |
| `1100` | admin settings, exports (`1110`), compile presets and the contact block (`1120`), static site options, per-document settings and state (`1140`), pinned addresses (`1141`), diagnostics |
| `1200` | Git remotes, push history |
| `1300` | project sources, import jobs, document provenance |
| `1400` | alternatives and ghosts (`1400`), the stash (`1410`) and its search (`1420`); Lab annotations are Phase 4 |
| `1500` | compendiums, members, references, overrides, links, field definitions, exports (`1500_compendiums`); `1510_events_compendium` adds `events.compendium_id`; `1520_mention_triggers` marks projects whose vocabulary changed (it follows 1500 because it watches its tables) |
| `1600` | agent keys, idempotency |
| `1700` | integrations: webhooks, their outbox and the feeder's cursor (`1700_webhooks`) |
| `1800` | editions: the imported books a project came from, their source files and chapters (`1800_editions`); Phase 7 EPUB import, range 1800 to 1819 |
| `1870` | mail: the relay's settings (`1870_mail_config`), the outbox (`1871_mail_outbox`), digest opt-ins (`1872_mail_digest`), Kindle addresses, previews and the send log (`1873_kindle`), the Kindle domain list, the digest's bound address and the sealed send-log address (`1874_mail_review`) |
| `1930` | the writing studio: goals with versions and the chapters finished (`1930_studio_goals`), sprints (`1931_studio_sprints`), nudges shown (`1932_studio_nudges`), private progress cards (`1933_studio_cards`), the progress email's opt-in and a third kind in the outbox (`1934_mail_progress`), a covering index on `daily_stats` for the dashboard (`1935_studio_index`) |
| `1910` | custom typography (F-085): `project_typography` and `user_typography` (`1910_typography`). Presence (F-244) has no table: it is memory only |

---

## Identity and access

### `users`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | TEXT PK | `usr_…` |
| `oidc_issuer` | TEXT | issuer URL |
| `oidc_sub` | TEXT | subject; `(issuer, sub)` unique |
| `email` | TEXT | unique (case-insensitive) among live users when not null; never used for login |
| `display_name` | TEXT | from `name` claim, user-editable |
| `avatar_color` | TEXT | `#rrggbb`, deterministic from id |
| `timezone` | TEXT | IANA name; stats days use it |
| `locale` | TEXT | `en`, `de`, … |
| `is_agent` | INT | 1 for API-only principals; never a session |
| `role` | TEXT | `admin`, `writer`, `reader`; derived from IdP groups on every login (an admin claimed on first run persists while no admin group is configured) |
| `created_at`, `updated_at`, `last_seen_at`, `deleted_at` | TEXT | |

Identity binding is `issuer|sub` (unique among live users; a removed account is refused at sign-in, never silently recreated). Email linking happens only once, to an account whose `oidc_sub IS NULL`, via a conditional `UPDATE … WHERE oidc_sub IS NULL` checked with `RowsAffected`. A lost race is a denial, not a merge.

### `sessions`

`id`, `user_id`, `token_hash` (SHA-256 of the cookie secret), `created_at`, `last_seen_at`, `expires_at`, `idle_expires_at`, `user_agent`, `ip_hash`, `revoked_at`.

### `api_keys`

`id`, `user_id`, `name`, `label` (human note, e.g. "Claude, compendium builder"), `prefix` (shown), `secret_hash`, `scope` (one of `read`, `write`, `admin`), `compendium_id` NULL (restrict to one compendium; no foreign key, so the table does not depend on the compendium tables), `project_id` NULL (restrict to one project), `parent_key_id` NULL (the key that made this one; a child never outlives its parent and is revoked with it), `is_agent`, `created_at`, `last_used_at`, `expires_at`, `revoked_at`.

A key cannot carry both a compendium and a project restriction (the `CHECK` stays). A project restriction reaches that project's *primary* Compendium too, looked up at each call, because the Compendium often does not exist yet when the key is made; a compendium restriction reaches no project. Agent keys are ordinary keys with `is_agent=1`, so the audit log and the UI can distinguish them.

### `idempotency_keys`

`key`, `principal_id` (`key:<api key id>` or `user:<user id>`), `op`, `request_hash` (op, method, path, sorted query and exact body), `response_json`, `status_code` (0 is a tombstone: it ran, and its response held a secret or was too large to keep), `created_at`, `expires_at`. PK `(principal_id, key)`; `WITHOUT ROWID`.

A retried write with the same key returns the stored response instead of applying twice. Only successful responses are stored. Expired rows are swept hourly.

### `audit_log`

`id`, `at`, `actor_id`, `action` (`auth.login`, `apikey.create`, `project.member.add`, `ai.key.set`, `export.run`, …), `target_type`, `target_id`, `ip_hash`, `detail_json`.

---

## Projects

### `projects`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | TEXT PK | `prj_…` |
| `owner_id` | TEXT | FK users |
| `compendium_id` | TEXT NULL | the project's single primary Compendium, or NULL |
| `title` | TEXT | required, ≤ 200 |
| `slug` | TEXT | URL-safe, from title; unique **per owner**, so a title never reveals another person's project; page routes resolve it among projects the caller can open |
| `kind` | TEXT | `novel`, `novella`, `short`, `collection`, `nonfiction`, `essay`, `blog`, `report`, `blank` |
| `subtitle`, `author_name`, `genre`, `language` | TEXT | metadata |
| `status` | TEXT | `idea`, `outlining`, `drafting`, `revising`, `finished`, `published` |
| `description` | TEXT | blurb or premise, Markdown |
| `word_goal` | INT | 0 = none |
| `deadline` | TEXT | date, optional |
| `cover_color` | TEXT | `#rrggbb` |
| `cover_path` | TEXT | unused: no code reads or writes it; the writer's own cover is a cover layout (`cover_layouts`, planned migration `0810`, see [COVERS.md](COVERS.md)) |
| `settings_json` | TEXT | numbering, typography, AI policy, export defaults |
| `view_passphrase_hash` | TEXT | optional application-level gate (Phase 7); not encryption |
| `created_at`, `updated_at`, `deleted_at`, `trashed_at` | TEXT | |

### `project_members`

`project_id`, `user_id`, `role` (`owner`, `editor`, `reviewer`, `reader`), `created_at`. PK `(project_id, user_id)`.

### `project_typography` and `user_typography`

How a project's writing and reading views are set (F-085), and a person's own override of it. `project_typography`: `project_id` (PK, cascades with the project), `settings_json`, `updated_by`, `updated_at`. `user_typography`: `user_id` (PK, cascades with the person), `settings_json`, `updated_at`. `settings_json` holds only the fields of a fixed schema (`font`, `size`, `line_height`, `measure`, `paragraphs`, `scene_break`: enums and bounded numbers, no CSS, no URL, no font file); a stored value that is no longer valid is dropped field by field when read. A project with no row is set as the application ships it. Nothing here reaches an export, compile, print or the static site. The schema and the reasons are in [p7-notes/typography.md](p7-notes/typography.md).

Presence (F-244) is deliberately not here: it is a table in the server's memory, never written to the database, a backup, an export, the mirror or the events table ([p7-notes/presence.md](p7-notes/presence.md)). Its one setting, `presence.share`, is an ordinary row of `user_prefs`.

### `project_templates`

`id`, `owner_id`, `name`, `kind`, `structure_json`, `settings_json`, `created_at`. Used by "duplicate as template".

### `compendiums`

A Compendium holds the shared material for one story world or subject, and exists independently of projects. It can be created for a book, for a future series, or purely as a reference. See [OPEN-QUESTIONS § 27](OPEN-QUESTIONS.md#27-cross-project-entities-decided).

`id` (`cmp_…`), `owner_id`, `name`, `slug`, `kind` (`fiction`, `nonfiction`), `description`, `settings_json`, `created_at`, `updated_at`, `deleted_at` (reserved, as on projects: nothing sets it), `trashed_at`, `archived_at`. Unique `(owner_id, slug)`.

`projects.compendium_id` is the project's single primary Compendium, so "at most one" is the column itself. SQLite cannot add a foreign key to an existing column, so triggers keep it honest: it must name a Compendium, and it is cleared when that Compendium is deleted.

### `compendium_members`

`compendium_id`, `user_id`, `role` (`owner`, `editor`, `reader`), `created_at`. PK `(compendium_id, user_id)`. Project membership does **not** grant Compendium access; it is granted explicitly, so a beta reader on one book cannot read the whole bible. There is no reviewer role here (an earlier draft had one): nothing comments on a bible entry, so a reviewer would only be a reader. An instance admin acts as owner of every Compendium, as for projects.

### `compendium_refs`

`id`, `owner_kind` (`project`, `compendium`), `owner_id`, `target_compendium_id`, `mode` (`reference`, `copy`), `created_by`, `created_at`. Unique `(owner_kind, owner_id, target_compendium_id)`.

A project can reference the whole of more than one other Compendium while keeping exactly one primary; a Compendium can reference another the same way (a shared cosmology, a common history, a house style). `owner_id` is polymorphic, so a trigger on `projects` and one on `compendiums` stand in for the cascade; deleting the target cascades by foreign key. Only `reference` is written today. References are followed once: a project sees its primary, the Compendiums it references itself, and nothing beyond.

### `project_entity_overrides`

`project_id`, `entity_id`, `name`, `summary` (NULL inherits the Compendium's), `fields_json` (only the keys the book changes), `notes`, `updated_by`, `updated_at`. PK `(project_id, entity_id)`. A per-book view of a shared entity: age at this point, role, appearance. The Compendium record is unchanged. The effective entity is the record with the override applied (see ARCHITECTURE, "Compendiums and shared material").

### `project_entity_links`

`project_id`, `entity_id`, `mode` (`reference`, `copy`), `created_by`, `created_at`. PK `(project_id, entity_id)`. One entity from a Compendium the project does not otherwise use (a cameo). Only `reference` is written today.

### `entity_field_defs`

`id`, `compendium_id`, `kind`, `key`, `label`, `type` (`text`, `number`, `date`, `choice`), `choices_json`, `position`, `created_at`. Unique `(compendium_id, kind, key)`. The custom fields of one kind of entity in one Compendium. An entity can hold values only for keys defined here.

### `compendium_exports`

`id`, `compendium_id`, `user_id`, `preset`, `format` (`md`), `filename`, `bytes`, `sha256`, `manifest_json` (the entity ids and hashes it held), `created_at`. Separate from `exports`, whose `project_id` is required.

---

## Documents

### `documents`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | TEXT PK | `doc_…` |
| `project_id` | TEXT | FK, indexed |
| `compendium_id` | TEXT | Reserved and unused: research notes are not documents (see `research_notes`) |
| `parent_id` | TEXT NULL | self-FK; NULL = root |
| `kind` | TEXT | `part`, `chapter`, `scene`, `note`, `front_matter`, `back_matter`; `research` is permitted by the CHECK but nothing writes it, research lives in `research_notes` |
| `title` | TEXT | required |
| `content` | TEXT | Markdown |
| `content_hash` | TEXT | SHA-256 of normalised content; used to skip duplicate revisions |
| `synopsis_short` | TEXT | one line, ≤ 300 |
| `synopsis_long` | TEXT | paragraph |
| `status` | TEXT | `idea`, `outline`, `draft`, `revise`, `done` |
| `position` | REAL | among siblings |
| `revision` | INT | increments on every accepted save; the client's concurrency token |
| `word_count`, `char_count` | INT | derived |
| `target_words` | INT | optional |
| `pov_entity_id`, `location_entity_id` | TEXT NULL | entities; no declared foreign key (documents predate them). NULL until scenes point at entities; a trigger clears them when an entity is deleted and `entity.merge` repoints them |
| `pov`, `location` | TEXT | free text, ≤ 200; what the outline shows and edits until Phase 3 migrates them to entities (see OPEN-QUESTIONS, Phase 2 backend decisions) |
| `content_at`, `content_by` | TEXT, TEXT NULL | when the body last changed and who changed it; `updated_at` also moves on a rename or a move. Empty/NULL for rows written without them; readers fall back to `updated_at` |
| `story_date`, `story_date_end` | TEXT | free-form ISO-ish; partial dates allowed (`1998-04`, `1998`) |
| `include_in_compile` | INT | default 1 |
| `ai_exclude` | INT | default 0; never send to any provider |
| `created_at`, `updated_at`, `deleted_at` | TEXT | |

Indexes: `(project_id, parent_id, position)`, `(project_id, kind)`, `(project_id, status)`, `(project_id, updated_at)`.

### `document_revisions`

`id`, `document_id`, `revision` (the document revision this text had), `content_hash`, `word_count`, `author_id`, `reason` (`autosave`, `manual`, `snapshot`, `restore`, `import`, `ai-accepted`, `alternative`), `label`, `note`, `created_at` (when the text became the document's head: what people see), `recorded_at` (when the row was written: what the autosave throttle reads).

The text itself is in **`revision_blobs`** (`document_id`, `content_hash`, `encoding` `identity` or `gzip`, `content` BLOB, `raw_size`), one row per distinct text per document, referenced by revision rows through `(document_id, content_hash)`. Blobs are per document, not global, so they go with the document and a purged project leaves nothing behind. (This replaces the `content`/`content_encoding` columns an earlier draft of this table carried; `zstd` is not used because the standard library has no encoder.)

Rules:
- An autosave revision is written when a save that changes the body arrives, the **previous** head differs from the newest revision's text, and that revision was written at least 5 minutes ago (`db.Now` clock). It records the text as it stood, stamped with the time it became the head and the person who saved it, so the end of an editing session is kept. The newest saved text is the document itself and shows as the current version.
- A manual snapshot is always written.
- Restoring writes a **new** revision with `reason='restore'`; history is append-only. The text being replaced is first kept as a `snapshot` revision (note `Before restore`, no label) unless some revision already holds it. `doc.split` does the same (note `Before split`). Those copies are not pinned: only a person's label pins a revision, so they thin like any automatic version.
- Content above 8 KB is gzip-compressed; identical content hashes are stored once per document and referenced.
- Pruning keeps every manual snapshot and every labelled revision forever, the revision most recently *written* for each document (the row the throttle reads), and everything from the last 30 days; older, it keeps the newest revision of each UTC day. Configurable through the `revisions.autosave_days` and `revisions.keep_daily` settings.

### `document_links`

`id`, `from_id`, `to_id` (NULL while unresolved), `link_text` (the target as written), `target_key` (its folded form, matched in Go because SQLite's `lower()` is ASCII-only: Unicode normal form C plus the full case fold, `text.FoldTitle`), `alias`, `ord`, `occurrences`, `ambiguous`, `snippet`, `kind` (`wiki`, `mention`, `embed`), `created_at`. Unique `(from_id, kind, link_text)`: the same key as `(from_id, to_id, kind, link_text)`, because `to_id` is a function of the text, and unlike it, it also covers unresolved links (a UNIQUE index does not compare NULLs).

Backlinks are a reverse index, not a separate table. Links in code, source blocks and `%%private notes%%` are not links.

### `tags` / `research_tags` / `document_tags`

`tags(id, project_id | compendium_id, name, name_key, color, created_at)`: exactly one owner, unique by folded name per owner (`name_key`, folded in Go), `color` one of a fixed list of tokens (`slate`, `clay`, `ochre`, `moss`, `teal`, `blue`, `plum`, `rose`). `research_tags(note_id, tag_id)` is the join in use (migration `0350_tags`); `document_tags(document_id, tag_id)` exists for manuscript documents and no op writes it yet.

### `research_notes`

(`0340_research`.) `id` (`rsn_…`), exactly one of `project_id` and `compendium_id`, `parent_id` (a folder), `kind` (`note`, `clipping`, `folder`), `title`, `body` (Markdown), `source_url`, `retrieved_on` (`YYYY-MM-DD`), `quote`, `status` (`inbox`, `reading`, `useful`, `archived`: the board column), `position` REAL, `revision`, `word_count`, `content_hash`, `created_by`, timestamps, `deleted_at`, `trash_batch`. **This replaces "documents of kind `research`" with a table of its own**: `documents.project_id` is required and a Compendium may have no project, and a research note must never reach the manuscript tree, the outline, the word counts or the compile. `research_text` / `research_fts` (`0920_research_search`) are its search index, kept in the same transaction.

### `scratchpads`

(`0360_scratchpads`.) `user_id`, exactly one of `project_id` and `compendium_id`, `content`, `revision`, `updated_at`; unique per person per owner; every foreign key cascades. Nothing reads another person's row.

### `saved_searches`

`id`, `user_id`, `name`, `query_json`, `created_at`.

---

## Review

### `comments`

(`0430_comments`.) `id` (`cmt_…`), `project_id`, `document_id` (NULL: a comment on the project), `parent_id` (NULL: the first comment of a thread; a reply names it, one level deep), `kind` (`comment`, `question`, `suggestion-note`), `author_id` (SET NULL when the account is deleted), `share_id` and `guest_name` (a guest of a share link; `author_id` is then NULL; a comment imported from a Word file has `guest_name` set to the Word author's name with `share_id` and `author_id` both NULL, which no link or account makes, and is shown with the author kind `label`; `share_id` has no foreign key because `share_links` is created later and the project cascade removes the rows), `body` (plain text, at most 10,000 characters, 2,000 for a guest), `status` (`open`, `resolved`; only a first comment has one), `resolved_by`, `resolved_at`, the anchor, `created_at`, `updated_at`, `edited_at`, `deleted_at`, `client_key` (`0470`: for a guest, a hash of name, text and place, to answer a double submit with the first).

The anchor, on the first comment of an anchored thread only: `anchor_state` (`none`, `active`, `orphaned`), `anchor_start` and `anchor_end` (UTF-16 code units of the body at `anchor_revision`: the text before the alternatives comment, LF line endings), `anchor_quote` (the Markdown source there now), `anchor_original` (what it was when the comment was made; never rewritten), `anchor_prefix` and `anchor_suffix` (up to 24 characters of source either side), `anchor_hash` (sha256 of the quote). It is the alternatives anchor under another name and is found again by the same `alts.Rebase`, run in the save's transaction (OPEN-QUESTIONS, "Phase 5 review and sharing decisions"). If the text is gone the state is `orphaned`: the last range is kept, not trusted, and the comment is listed in the review queue; it is never moved onto a lookalike. A deleted comment keeps its row so the thread keeps its shape: `body` is emptied, `deleted_at` set and the anchor dropped.

Comments are **not document content**: not in `documents.content`, the search text (`doc_text`), a word count, the mirror, a Git push, an export or an AI context. They are in backups (the database file) and go with the project (cascade).

### `comment_mentions`

`comment_id`, `user_id`; PK both. The members a comment mentions, resolved when it was written (`@name` in the text is matched to a display name of someone who can read comments, and ids may be sent). Both cascade.

### `suggestions`

(`0440_suggestions`; `1820_review_author_labels` adds `author_label`.) `id` (`sug_…`), `project_id`, `document_id`, `author_id`, `author_label` (empty, or the name of a Word author when the suggestion came from the tracked changes of an imported .docx: then `author_id` is NULL, nothing is notified, and the label is text only, never matched to an account), `status` (`open`, `accepted`, `rejected`, `withdrawn`: DATA-MODEL's first draft had no `withdrawn`), `base_revision`, `ops_json`, `note`, `created_at`, `updated_at`, `decided_at`, `decided_by`, `applied_revision`, `revision_id` (the history entry an acceptance wrote). `ops_json` is the ordered list of changes: `kind` (`replace`, which also deletes, or `insert`), `u1` and `u2` (UTF-16 units at `base_revision`), `original` (the text replaced), `before` and `after` (the paragraph context, or for an insertion the 24 characters either side), `text`. The body of the document is untouched while a suggestion is open.

### `share_links`

(`0450_share_links`.) `id` (`shl_…`), `project_id`, `label`, `scope` (`read`, `comment`; DATA-MODEL's draft said `role`), `token_hash` (hex SHA-256 of the **secret half** of the token; the token is `<id>.<secret>`, 256 bits of secret, shown once), `passphrase_hash` (argon2id, or NULL), `whole` (`0470`: the link shows the whole manuscript; otherwise only its `share_link_docs`, and a link whose documents are all gone shows nothing), `allow_download`, `ghosts` (`omit`, `keep`), `expires_at`, `views` (openings of the contents), `comment_count`, `failed_unlocks` (wrong passphrases ever tried; for the owner), `locked_until` (no longer written), `created_by`, `created_at`, `updated_at`, `revoked_at`, `last_used_at`. `views` and `last_used_at` are written in batches (`share.Meter`), not per request. `share_link_docs(share_id, document_id)` names the documents a link shows, each with its descendants; none means the whole manuscript. Both cascade with the project. `share_unlock_failures(share_id, bucket, failed, locked_until, last_at)` (`0470`) counts wrong passphrases per link and per client address bucket (an IPv4 address or an IPv6 /64, hashed) and holds the time the bucket may try again.

### `notifications`

(`0460_notifications`.) `id`, `user_id`, `project_id`, `kind` (`mention`, `reply`, `comment`, `resolved`, `suggestion`, `suggestion-decided`), `comment_id`, `suggestion_id`, `document_id`, `actor_id` (NULL for a guest), `created_at`, `read_at`. Ids only: the text shown is read from the comment when the list is built, so a deleted or reworded comment leaves nothing here and a person who has left the project sees nothing. `count` and `share_id` (`0470`): what guests write in one thread within an hour is one row with a count, and rows that came from guests are kept apart with a cap of 100 so they cannot push a person's others out. At most 500 other rows a person (the oldest go as new ones arrive); read ones are swept after 60 days, all after a year. Nothing is emailed or pushed.

---

## Entities, continuity, timeline

### `entities`

`id` (`ent_…`), `compendium_id` and `project_id` (exactly one is set: a Compendium entity, or a project-local one; no op writes the latter yet), `kind` (`character`, `place`, `item`, `org`, `concept`), `name`, `name_key` (the name folded in Go, `text.FoldTitle`, because SQLite's `lower()` is ASCII-only), `sort_name`, `sort_key`, `summary`, `body` (Markdown; the draft called it `description`), `fields_json` (custom field values by key, as canonical strings), `color`, `revision`, `created_at`, `updated_at`, `trashed_at`.

A name is unique among a Compendium's live entities, ignoring case and spacing (a partial unique index on `(compendium_id, name_key)` excluding trashed ones). The kind is fixed once an entity exists. `first_seen_doc_id` is not a column: first appearance is per project and comes from the mention index.

### `entity_aliases`

`id`, `entity_id`, `alias`, `alias_key`, `notes`, `created_at`. Unique `(entity_id, alias_key)`. Across a Compendium's live entities an alias may not equal another's name or alias; the ops enforce that in the writer's transaction and name the other entity. (There is no `is_primary` column: nothing reads one.)

### `entity_relations`

`id`, `compendium_id`, `from_entity_id`, `to_entity_id`, `kind` (`family`, `ally`, `rival`, `mentor`, `romantic`, `member`, `custom`), `label` (how it reads from the source), `inverse_label` (how it reads from the target), `symmetric`, `notes`, `created_at`. Unique `(from_entity_id, to_entity_id, kind, label)`; both ends are in the same Compendium. A relation scoped to one book (`project_id`) is not built.

### `entity_redirects`

`from_id` (PK), `to_id`, `compendium_id`, `from_name`, `merged_by`, `summary_json` (counts), `snapshot_json` (the merged entity's text as it was), `created_at`. Written by `entity.merge`; the old id resolves to `to_id` for a year (`entities.RedirectTTL`). Older redirects that pointed at the merged entity are re-pointed, so it is always one hop.

### `entity_text` and `entity_fts`

`entity_text(id, entity_id UNIQUE, compendium_id, name, aliases, summary, body)` holds the plain text the index reads; `entity_fts` is an external-content FTS5 table over it (`name, aliases, summary, body`), kept in step by triggers. The entity ops rewrite the `entity_text` row in the same transaction as every change to the entity or its aliases; `scriobh search rebuild` re-derives all of it.

### `entity_mentions`

`document_id`, `entity_id`, `project_id`, `count` (occurrences of the name or an alias in the prose; 0 for a document that only links the entity), `names` and `aliases` (the split of `count`), `explicit` (occurrences of `[[links]]` that name the entity), `ambiguous` (another entity in the project's scope answers to the same words), `first_pos` (rune offset of the first occurrence in the search text), `snippet` (plain text around it). Primary key `(document_id, entity_id)`; everything cascades with its document, entity and project.

One aggregate row per document and entity, not one per occurrence as the first draft had it (`surface`, `start_offset`, `end_offset`, `source`): the places of the occurrences are computed from the Markdown when `mention.list` is asked for spans, so the index is a few rows per document however long the text is. There are no manual mentions; explicit `[[links]]` are the explicit mention. The table is derived: deleting every row is safe, `scriobh mentions rebuild` brings it back from `doc_text`.

### `mention_state`

`project_id` (PK), `stamp`, `indexed_stamp`, `indexed_at`. `stamp` is an opaque random token that changes, in the same transaction as the change, whenever anything a project's vocabulary is made of changes (an entity's name or trash state, an alias, an override, a reference, a link, the primary Compendium, a Compendium's trash); triggers in `1520_mention_triggers` do it, so no writer can miss it. `indexed_stamp` is the stamp the last complete scan was built against; they differ while a rescan is owed (`indexing: true` on every mention read). The stamp is compared for equality and is neither a clock nor an id, so a rolled-back or previewed change cannot collide with a committed one.

### `timeline_events`

`id` (`tle_…`), `compendium_id`, `title`, `description` (Markdown), `story_date`, `story_date_end` (canonical `YYYY`, `YYYY-MM`, `YYYY-MM-DD` or `YYYY-MM-DDTHH:MM`, a year may be negative; empty for undated or for a moment), `story_label` (free text shown with the date, never used to order), `sort_start` and `sort_end` (fixed-width keys computed in Go from the dates, so SQL and Go order the same: see OPEN-QUESTIONS), `location_entity_id` (SET NULL), `revision`, `created_at`, `updated_at`, `trashed_at`. The first draft's `participants_json`, `narrative_doc_id` and `narrative_order` are replaced by the tables below, because a Compendium is shared by several books and each book orders and tells things for itself.

### `timeline_event_people`

`event_id`, `entity_id`, `role` (up to 60 characters), `created_at`. Primary key `(event_id, entity_id)`; the entity cascades. An event's participants and place are entities of the event's own Compendium.

### `timeline_event_docs`

`event_id`, `document_id`, `project_id` (the document's, kept so a book's view is one indexed read), `created_at`. The scenes that tell an event; cascades with the event, the document and the project.

### `timeline_narrative`

`project_id`, `event_id`, `position` (REAL gap). A book's own order of events, when it sets one; absent rows mean the order is derived from where the book's scenes sit. Cascades with the project and the event.

### Scene metadata

`documents.pov` and `documents.location` stay as free text (the display fallback, never deleted); `pov_entity_id` and `location_entity_id` name the entity when one is linked. They are cleared if the entity is deleted (a trigger of `1500`) and repointed by a merge. `documents.story_date` and `story_date_end` are read by the timeline as partial dates; text that is not a date is kept and treated as undated.

---

## AI

Migrations `0600` (providers and prices), `0610` (policy, and the never-send flags), `0620` (runs, usage, budgets), `0690` (a policy flag for personal providers) and `0695` (the `unsettled` run status; it rebuilds `ai_runs`). The AI recipes' own tables are in `0630`. Recipes are code (`internal/ai`), not rows: `ai_recipes` was dropped from the plan, so a prompt's wording is reviewed in the repository. Settings that are instance-wide and small (`ai.enabled`, `ai.chains`, `ai.currency`) live in the `settings` table.

### `ai_providers`

`id` (`aip_…`), `user_id` NULL for an instance provider an administrator manages, else the one person it belongs to (cascades with the user); `kind` (`openai_compatible`, `openrouter`, `opencode`, `ollama`), `name` (unique per owner, case folded), `base_url`, `api_key_cipher` and `key_hint` (the last four characters), `default_model`, `models_json` (the provider's list with context size, longest answer and price, plus models typed in, marked `manual`), `models_fetched_at`, `settings_json` (extra headers from an allow-list, OpenRouter routing, the OpenCode user name, Ollama `keep_alive`), `enabled`, `local` (declared by an administrator), `timeout_seconds`, `max_concurrency` (0 for the server's default), `created_by`, `created_at`, `updated_at`.

The key is sealed with `internal/secrets` under the context `ai_providers/<id>/<instance|user:<id>>/api_key/<normalised address>`: a ciphertext copied to another row, another owner or another address does not open, and changing the address drops the key. No op returns it.

### `ai_prices`

`(provider_id, model)` primary key, `provider_id` empty for any provider; `input_micros`, `output_micros` per million tokens in micro-units of the instance currency; `updated_at`. A model with no row here and no price in its provider's list has an unknown price.

### `ai_policies`

One row per project (none means mode `off`): `mode` (`off`, `ask`, `local_only`), `actions_json`, `providers_json`, `models_json`, `chains_json` (per recipe, action or `default`: the providers and models to try, in order), `redaction` (`off`, `entities`, `names`), `agents_may_use` (an API key may run AI on the project only when 1), `allow_personal_providers` (0 by default: with no provider list only instance providers are used; 0690), `budget_cost_micros` and `budget_tokens` (the owner's own monthly cap for the project, 0 none), `style_guide`, `updated_by`, `updated_at`. Cascades with the project.

`documents.ai_exclude` (since 0300), `research_notes.ai_exclude` and `entities.ai_exclude` (0610) mean never send.

### `ai_runs`

`id` (`air_…`), `user_id`, `project_id`, `document_id`, `recipe`, `recipe_version`, `provider_id`, `model`, `via` (`session`, `apikey`), `status` (`running`, `ok`, `cancelled`, `error`, `blocked`, `unsettled`: the run happened but its settlement could not be written, so its reservation counts as spent for the month), `error_code`, `input_tokens`, `output_tokens`, `usage_reported` (1: the provider's counts, 0: an estimate), `cost_micros` (what was charged against the money caps), `cost_known`, `own_cost` (the person's own provider paid), `reserved_micros` and `reserved_tokens` (held while `running`), `currency`, `local`, `attempts`, `duration_ms`, `payload_hash`, `output_hash`, `scope_json` (the chips by kind, label and token count, never text), `created_at`, `finished_at`.

**No prompt and no answer text is stored.** There are no foreign keys on this table or the next: deleting a project or a person must not reset a month's spend. Transcripts are not built.

### `ai_usage_daily`

`user_id`, `project_id`, `day` (UTC), `runs`, `input_tokens`, `output_tokens`, `cost_micros` (charged against the money caps: instance-paid providers only), `own_micros` (paid by the person's own provider). PK `(user_id, project_id, day)`.

### `ai_budgets`

`(scope, scope_id)` primary key, `scope` `instance` (id empty), `user` or `project`; `max_cost_micros`, `max_tokens` (0 none), `updated_by`, `updated_at`.

A cap is checked in the same writer transaction that records the run: what was spent this month (`ai_usage_daily`) plus what is reserved by the `running` rows plus this request's worst case must fit under every cap that applies, so two concurrent requests cannot both slip under it.


### The recipes' tables (`0630_ai_work`; triggers `0640_ai_work_cleanup`, `1530_ai_work_compendium_access`)

Unlike `ai_runs`, these hold text derived from the manuscript, so they have foreign keys and go with the project, the document or the person. Each is the explicit exception to "no answer text is stored", with a way out.

- `ai_pending(run_id PK, project_id, user_id, document_id, recipe, kind edit|alternatives|summary|chat, doc_revision, output_hash, items_json, created_at, expires_at)`: what a run produced that can be accepted, for 30 minutes. `output_hash` equals `ai_runs.output_hash`; each item in `items_json` carries the hash of its own text, the original range (UTF-16 of the body) and context.
- `ai_accepts(run_id, item_id PK, project_id, user_id, action accepted|rejected, revision, revision_id, created_at)`: a text-free ledger written in the transaction of the write; keeps accept idempotent.
- `ai_reviews(id, project_id, user_id, document_id, run_id, recipe, kind critique|continuity|extraction, doc_revision, dropped, created_at)` and `ai_notes(id, review_id, position, category, severity, body, example, trimmed, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, sources_json, sheet_json, status open|accepted|dismissed, result_ref, resolved_at)`: a review's notes, findings or proposed entity sheets; `start_u16` is -1 when the quote was not found. Private to `user_id`.
- `ai_marks(id, project_id, user_id, document_id, run_id, source model|local, kind, reason, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, has_fix, fix, status, created_at, resolved_at)`: the Lab's marks. `has_fix=0` is an observation; `fix=''` with `has_fix=1` deletes the quote. A local batch has a run id beginning `lab_`.
- `ai_jobs(id, project_id, user_id, kind rolling_summary, status open|done|cancelled, plan_json, estimate_json, …)`: the plan and position of a rolling summary.
- `ai_chats(id, project_id, user_id, title, …)` and `ai_chat_turns(chat_id, seq, role, content, run_id, created_at)`: a conversation the person chose to keep.
- Triggers: a person's rows on a project go when their membership ends or drops below editor (0640); their continuity and extraction reviews go when their Compendium access ends (1530); a note's body, example, quote and sheet are size-limited (0640). `ai_pending.items_json` is sealed (`s1:` prefix) when the server has a secret key.
- `ai_user_defaults(user_id, class cheap|strong|fast|local, chain_json, updated_at)`: a person's default chains; the project's and the instance's class chains are `class:<class>` keys of `ai_policies.chains_json` and the `ai.chains` setting.

---

## Goals and stats

Migration `0700`. Everything here is per person; no op takes a user id from the request.

### `goals`

`id`, `user_id`, `project_id`, `kind` (`daily`, `weekly`, `monthly`, `project`, `document`), `target_words`, `basis` (`added` by default, `net` optional), `start_date` (local date it was set), `end_date` (optional, `''` for none), `created_at`, `updated_at`.

v1 writes `daily` and `project` only, one of each per person per project (a partial unique index on those two kinds; the later kinds are not constrained). The per-document target stays on `documents.target_words`. The project's shared target stays on `projects.word_goal` and `deadline` (`project.update`); a `project` goal row is the person's own target and deadline and wins in their own `stats.project`. No op writes `basis = 'net'`.

Goals are evaluated against **words added**. Cutting 500 words is work; the net change is stored and shown separately, never used to fail a day.

### `writing_sessions`

`id`, `user_id`, `project_id`, `document_id` (the first document saved; a session may span several), `day` (local date in the person's zone when the session began), `tz` (the zone that date was read in), `started_at`, `ended_at` (the last save), `words_added`, `words_net`, `active_seconds`, `saves`, `kind` (`writing`, `sprint`).

A session continues while saves arrive within 30 minutes and the local day and zone are unchanged; otherwise the next save starts a new one. `active_seconds` adds the gap between consecutive saves, each capped at 5 minutes, so minutes mean time spent writing, not first save to last. Because every session carries its own `day`, a later change of time zone never re-buckets history.

Words added counts insertions measured against a per-session baseline; words net is the exact difference of the stored word counts. Keep both, because cutting 500 words is work and a net-only stat punishes revision. The counting rule and its approximations are in [OPEN-QUESTIONS § 39](OPEN-QUESTIONS.md#39-phase-2-goals-and-import-decisions--decided).

### `writing_session_docs`

`session_id`, `document_id` (foreign key, cascades), `baseline_text`, `baseline_words`, `carried`, `credit`, `last_hash` (hash of the text the session last left, to notice other people's changes), `touched_at`. PK `(session_id, document_id)`. The text credit is measured against (the document as the session first saved it, moved forward when credited words leave the text), the credit banked before that baseline, and the total credit so far (`credit` never decreases). Only the person's latest session keeps rows here: they are deleted when the next session starts, when the person leaves the project (trigger), when the document is purged, and by any save once untouched for an hour.

### `daily_stats`

`user_id`, `project_id`, `day` (local date in the person's zone), `words_added`, `words_net`, `minutes`, `sessions`, `active_seconds`, `goal_target`, `goal_hit`, `updated_at`. PK `(user_id, project_id, day)`. Maintained in the same transaction as the save, and recomputable from sessions (`goals.Recompute`, `scriobh stats recompute`): it is a plain sum of the sessions of that day.

A streak is consecutive days with `goal_hit = 1` (words added at or above that day's target). Merely opening the document does not extend it, and today does not break a streak until the day ends. `goal_target` follows the current daily goal until the day is hit, then is frozen: raising the goal later never takes a finished day away. The day boundary is the person's timezone when the save was made.

### The writing studio

Migrations `1930` to `1935`. Everything here is per person; no op takes a user id from the request. Nothing is a stored person-level total: every figure is summed from `daily_stats` and `writing_sessions` (above) over the projects the caller may count, so a project that is locked, in the trash or no longer the person's contributes nothing to any aggregate, and `scriobh stats recompute` rebuilds what the dashboard reads.

#### `studio_goals`, `studio_goal_versions`

`studio_goals(id, user_id, project_id, period, measure, start_date, ended_on, archived_at, completed_on, created_at)`: `period` is `day`, `week`, `month` or `total`; `measure` is `words`, `sessions`, `chapters` or `minutes`; `project_id` is empty for every project the person may read (no foreign key for that reason: a trigger removes a project's goals with the project). `ended_on` is the local date it was archived; the last day it applied is worked out when it is read, for the reader (that date if its period was met by what they may count, else the day before). `studio_goal_versions(goal_id, version, target, deadline, from_date, progress_at, created_at)` holds every target a goal has had: a change adds a row, nothing is edited. `from_date` is the local date it took effect and `progress_at` the person's progress in that period at that moment. `completed_on` is the day a total goal was first seen reached; it is never cleared by a cut (only by raising the target), is written by the owner's own reads, and is not shown to a viewer who cannot count every project the person has written in. A period is judged by the versions in force in it: it is met if the total reached the target then in force at some point in it, so a raise after a period was met never takes it away (the rule `daily_stats` already follows for a day). The older `goals` table stays for the Goals page (`goal.set`), and its goals are shown on the dashboard beside these, read-only.

#### `studio_chapters_done`

`(user_id, document_id)` PK, `project_id`, `day`, `created_at`: a document of kind `chapter` set to `done` for the first time by a person, on their local day. What a chapters goal counts. A document purged takes its row.

#### `studio_sprints`

`id`, `user_id`, `project_id` (cascades), `day`, `tz`, `planned_minutes` (0 is an open session timer), `target_words`, `started_at`, `closes_at`, `state` (`running`, `completed`, `stopped`, `abandoned`), `ended_at`, `words_added`, `words_net`, `saves`. One running sprint per person (a partial unique index). The save loop adds each save's credit to the day and, in the same transaction, to the running sprint of that person on that project while `now <= closes_at`, so each word is counted once in the day it was written on and once in the sprint it was written during. A sprint that runs past local midnight holds the words of two days: it is not a part of either, it reconciles with the sum of the days it touches over the time it ran, and no day holds a word twice. It counts on the day it began for the best sprint and the time-of-day statistics. A sprint left running is read as completed after `closes_at` (the planned end and two minutes, or four hours for an open timer) with no write and no timer; the next sprint write settles the row, and a restore closes every running sprint.

#### `studio_nudges_seen`, `studio_cards`

`studio_nudges_seen(user_id, day, nudge)`: the id of a nudge shown on a local day; at most three a day are offered, none twice, rows older than 60 days go when a new one is written. The switch is the preference `studio.nudges` (`user_prefs`), on by default. `studio_cards(id, user_id, token_hash, label, show_projects, expires_at, revoked_at, views, last_viewed_at, created_at)`: a private progress card; only the SHA-256 of the secret half of its token is stored. `views` is written at most once a minute from an in-memory count (and at shutdown); rows revoked or expired more than 90 days ago are deleted hourly.

#### `daily_stats_studio`

An index on `daily_stats(user_id, project_id, day, words_added, words_net, active_seconds, sessions, goal_hit)` (1935): the dashboard reads a person's rows across all their projects and years, and with the figures in the index that is an index scan, not a table lookup for each of the 36,520 rows of five years over 20 projects (the read went from 34 to 17 ms for 30 days, 70 to 52 ms for all time). The save rewrites one more index entry with the day's row; the cost is not measurable in `doc.update` (3.07 ms with it and 3.05 to 3.08 without, interleaved).

#### `mail_progress`

`(user_id)` PK, `frequency` (`weekly`, `monthly`), `address` (the account address it was turned on for), `last_period` (the ISO week `2026-W10` or month `2026-03` already handled, sent or empty), `enabled_at`, `updated_at`. Off by default: no row. `mail_outbox.kind` gains `progress` (the table was rebuilt for it, 1934: rows and indexes carried over).

---

## Files

### `attachments`

(`0800_files`.) `id` (`att_…`), exactly one of `project_id` and `compendium_id`, `sha256`, `bytes`, `mime` and `ext` (detected from the bytes), `kind` (`image`, `pdf`, `text`), `filename` (a display name, never a path), `alt`, `width`, `height`, `created_by`, timestamps. Unique `(owner, sha256)`: deduplicated per owner. There is no `path` column: the file is `files/projects/<id>/attachments/<sha256>.<ext>` (or `files/compendiums/<id>/…`), rebuilt from the row.

`attachment_refs(attachment_id, ref_kind, ref_id)`: where a `![](/files/att_…)` link was found when a document (`doc`) or a research note (`research`) was saved; only an attachment of the same owner counts. `file_usage(scope, bytes, files)`: counters for `instance`, `user:<id>` and `owner:<id>`, kept by triggers on `attachments` so a cascading purge is counted; the upload checks them in its own transaction. A delete removes the blob after the row is gone and committed, unless the row is referenced (then it is refused unless forced).

### `webhooks`, `webhook_deliveries`, `webhook_state`

A webhook belongs to a project (deleted with it): `name`, `url`, `events_json` (a list of `document.updated`, `comment.created`, `suggestion.created`, `export.completed`), `include_text`, `enabled` and `disabled_reason`, `secret_sealed` (the signing secret under `internal/secrets`, bound to `webhooks/<id>/secret`), `secret_hint`, `prev_secret_sealed` and `prev_secret_until` (a rotated secret that still signs), `failure_streak`, and what the last delivery was. `webhook_deliveries` is the outbox: `event`, `event_seq` (the `events` row it came from; `UNIQUE(webhook_id, event_seq)`), `doc_id` (for coalescing saves), `payload_json` (ids and counts, never text), `state` (`pending`, `delivered`, `dead`), `attempts`, `next_attempt_at`, `last_status`, `last_error` (a failure kind), `last_excerpt` (a cleaned 160-character piece of a failing answer). `webhook_state` is one row: the feeder's `cursor` over `events` (seeded at the newest event) and a count of events `dropped` for want of room. A backup holds all three; no export, mirror file or Git push does.

### `mail_config`, `mail_outbox`, `mail_digest`, `kindle_addresses`, `kindle_previews`, `kindle_sends`

`mail_config` is one row (`id = 1`), off by default: `enabled`, `host`, `port`, `security` (`starttls`, `tls`, `none`), `allow_private`, `tls_ca_pem` (a certificate authority's public certificate), `username`, `password_sealed` (under `internal/secrets`, bound to `mail_config/1/password/<host>:<port>/<user>`, so it opens only for the address and user it was entered for), `from_address`, `from_name`, `max_attachment_mb`, `disabled_reason`, `restore_paused` (a restore's own mark; only an administrator switching mail on clears it), and the last test's time and class. `mail_outbox` is the queue: `kind` (`kindle`, `digest`, `progress`), `person_id`, `project_id`, `recipient`, `recipient_domain`, `subject`, `body`, the attachment's `file_name`, `file_type`, `file_bytes` and `file_sha256` (the file itself is `mail/outbox/<id>` under the data directory, mode 0600), `dedupe_key` (unique when set), `window_since` and `window_until` (a digest's window of notifications: it is built again from them when it is sent), `message_id`, `state` (`pending`, `sending`, `sent`, `dead`), `attempts`, `next_try_at`, `data_started_at` (set just before the body is sent: a crash after it is `delivery_unknown`), `last_error` (a failure class, never a relay's words), `created_at`, `finished_at`. A finished row's `recipient`, `subject`, `body` and `file_name` are blanked at once; the row is removed after 14 days. `mail_digest(user_id, frequency, address, last_period, last_cursor, enabled_at, updated_at)` is a person's opt-in, the account address it was made for (a digest stops when the account's address differs) and where their next digest starts. `mail_config.kindle_domains` is the administrator's allow-list of Send-to-Kindle domains (empty is the default, `kindle.com` and `free.kindle.com`). `kindle_addresses(user_id, address, ...)` is a person's own address. `kindle_previews` holds the hash of a one-time token with the person, project, address and manifest hash it is bound to. `kindle_sends` is the per-project log: who, the manifest hash, the file's SHA-256, name and size, the recipient's domain, and `recipient_sealed`, the full address sealed with the instance key (bound to `kindle_sends/<id>/recipient`), shown only to the project's owner and administrators. A backup holds all of it (the password sealed; the key is never in a backup) and a restore switches mail off and empties the queue; no export, mirror file or Git push holds any of it.

### `exports`

`id`, `project_id`, `user_id`, `preset`, `format` (`md`, `html`, `docx`, `epub`, `pdf`), `filename`, `bytes`, `sha256`, `manifest_json` (included doc ids + hashes), `created_at`.

A compile (`export.compile`) records its preset's id in `preset` and the file's format in `format`; its `manifest_json` adds `preset_id`, the resolved `options`, `input_hash` (a hash of the preset, the options, the project's title and byline, the contact block and every included document's id and compiled-text hash), `words`, `warnings` (a count), `images` (a count) and, when documents were left out, `not_compiled` (their ids). The contact block is never in it. A submission bundle (a zip) has no format of its own in the constraint: it is recorded as `docx` with `preset` `submission/<preset id>`, and `manifest_json` has `"bundle": "submission"`.

### `compile_presets`

A custom compile preset (migration `1120`). `id` (`cpr_…`), `project_id`, `name` and `name_key` (folded; unique per project), `base` (the built-in preset it started from), `options_json` (every option, resolved, so a later change to a built-in never alters it), `created_by`, `created_at`, `updated_at`. At most 50 per project. The built-in presets (`shunn-us`, `shunn-us-courier`, `uk-manuscript`, `uk-manuscript-courier`, `styled-docx`, `epub3`, `plain-markdown`) are code, not rows.

### `manuscript_contact`

The contact block of a manuscript title page (migration `1120`), one row per project: `name`, `surname` (for the running header; empty means the last word of the byline), `address` (lines separated by newlines), `email`, `phone`, `updated_at`. Personal data: it is written into manuscript files and nowhere else, and never into an export's manifest, an audit entry, an event, the mirror, a Git push or a stored idempotent response (`compile.contact.set` and `compile.preview`, whose responses hold it, are not replayed).

### `site_options`, `site_docs`, `site_state`

Static site publishing (migration `1140`). `site_options(project_id, options_json, updated_at)`: a project's options as one JSON document, merged over the defaults when read (an option added later keeps its default). `site_docs(document_id, project_id, role, slug, slug_key, published_at, excerpt, tags_json, updated_at)`: what the site needs of a document and the manuscript does not have: `role` (`auto`, `post`, `page`, `skip`), an address (`slug`, with `slug_key` its folded form, unique per project when set), a publication date (RFC 3339, UTC; empty means the date the document was created, with a warning), an excerpt the owner wrote for the site, and tags. `site_state(project_id, build_*, publish_*, publish_dir)`: when the site was last built and last published, with counts and two hashes each (`site_hash` over every file and path, `input_hash` over what it was made from), and the folder name under the sites folder, kept once chosen. `site_docs.pinned_slug` (migration `1141`) is the address the document was given the first time the site was built or published, kept so that it does not move; a row may exist only to hold it. All three cascade with the project (and `site_docs` with its document). None is in the mirror, a Git push or an event: the site is generated from the manuscript and these rows, and is never a second writer. A build is also recorded in `exports` (`format` `html`, `preset` `site`, a manifest of the documents and hashes, no text) because the format constraint has no zip value.

### `project_gates`, `project_unlocks`, `project_unlock_failures`, `project_gate_key_use`, `project_gate_key_ok`

The project passphrase (migration `1850`; [p7-notes/passphrase.md](p7-notes/passphrase.md)). A gate is not encryption: the text is stored as always. `project_gates(project_id, pass_hash, epoch, idle_minutes, allow_keys, set_by, created_at, updated_at)`: one row per protected project, none for a project with no passphrase. `pass_hash` is argon2id (`internal/passkit`, the share links' parameters), never selected by a project query; `epoch` counts passphrase changes, so a new passphrase voids every unlock at once; `idle_minutes` is 1 to 720 (default 30); `allow_keys` is 0 by default (an API key is refused while a passphrase is set). `project_unlocks(session_id, project_id, epoch, unlocked_at, last_used_at)`: an unlock lives on a session and is valid for the epoch it was made in; it goes with the session or the project, and the gate ignores a row of a revoked session. `last_used_at` is written at most once a minute. `project_unlock_failures(project_id, bucket, failed, locked_until, last_at)`: wrong tries per bucket: `ses:` a session and `user:` a person are the lockout; `ip:` (a keyed hash of an IPv4 address or IPv6 /64) is soft: six more free tries, and its wait applies only to a caller with a wrong try of their own. `project_gate_key_use(key_id, project_id, uses, refused, first_at, last_at)`: what each API key has done to a protected project. `project_gate_key_ok(project_id, key_id, epoch, how, approved_by, created_at)` (migration `1852`): the keys a protected project lets through when its owner accepts keys; `how` is `minted` (made by a session that had the project unlocked) or `owner`; an approval is valid for the epoch it carries, so a new passphrase voids them all.

### `compendium_changes`

A recorded merge or split of Compendiums (migration `1851`): `id` (`cch_…`), `kind` (`merge`, `split`), `source_id` and `target_id` (both cascade with their Compendium), `actor_id`, `created_at`, `fingerprint` (of the plan that was applied), `summary_json` (counts) and `before_json` (the before-state: every entity moved with its name and revision, every entity merged away with its whole sheet, every relationship, event person, event place and alias dropped, field definitions copied, keys repointed, references added). Written in the move's own transaction; read with `compendium.change.list` and `compendium.change.get`; it is a record, not an undo.

---

## Events and configuration

### `events`

`seq INTEGER PRIMARY KEY AUTOINCREMENT` (commit order), `type`, `project_id`, `document_id`, `compendium_id`, `actor_id`, `payload_json`, `created_at`. Indexes `(project_id, seq)` and `(compendium_id, seq)`. A change to a Compendium carries its id; the mirror follows it to the Compendium's own folder and to every project that uses it. Rows are kept 90 days: the server's hourly sweep removes older ones in chunks of 2,000 (`events.Sweep`), never the newest row (readers take `max(seq)` as now) and, while a webhook is on, never a row its feeder has not read. Before Phase 6 nothing removed them, so a long-used instance shrinks its log at the first sweep after the upgrade.

Payloads carry ids and counts, never prose.

### `settings`

`key` TEXT PK, `value` TEXT, `updated_at`. Instance configuration that can be edited in the UI. Secrets never live here in the clear; they stay in env or the key file, or are sealed with the instance key (below).

Two rows belong to desktop mode (`SCRIOBH_DESKTOP=1`; `internal/domain/desktop`). Neither exists on an instance that never ran in it. No migration: both are JSON values in this table, so a backup carries them.

| Key | Value |
| --- | --- |
| `desktop.profile` | The one local profile: `sub` (a random id made once: the subject of every ID token the embedded issuer signs), `name`, `email` (optional), `created_at`. Made once, at the first run, and never replaced by the form. |
| `desktop.idp` | Which identity provider the instance signs in through: `mode` (`embedded` or `external`); `external` (issuer, client id, the client secret **sealed** with the instance key for the context `desktop.idp.client_secret`, group names, an optional fixed port); `verified` (the identity an administrator proved at that provider, the administrator's id and a hash of the settings it was proved with); `linked` (the account that was moved to the external identity, so the way back can move it again); `pending` (a switch to make at the next start); `last_problem`. |

The account of the local profile is an ordinary `users` row with `oidc_issuer = 'urn:scriobh:desktop-profile'` (a fixed string, because the embedded issuer's URL changes with the port) and `oidc_sub` the profile's `sub`; its `email` is NULL when none was given. The switch to an external provider rewrites that row's `oidc_issuer` and `oidc_sub`, in one transaction at start, and the way back rewrites them again; no other row is touched and nothing is matched by email address. Audit actions: `desktop.profile.created`, `desktop.idp.set`, `desktop.idp.verify`, `desktop.idp.switch`, `desktop.idp.cancel`, `desktop.idp.applied`.

### `user_prefs`

`user_id`, `key`, `value`. PK `(user_id, key)`. Theme, editor font, measure, motion, last document per project.

---

## Git sync and imports

### `git_remotes`

| Column | Notes |
| --- | --- |
| `id` | `grm_…` |
| `project_id` | FK, indexed |
| `name` | "Backup", "Public mirror" |
| `url`, `branch`, `path_prefix` | `https://…` or `ssh://…`; default branch `main`; optional subdirectory |
| `auth_kind` | `ssh_deploy_key`, `https_token` |
| `secret_cipher` | Private key or token, AES-256-GCM |
| `public_key` | Shown to the user for a deploy key |
| `known_hosts` | Pinned host key, set on first successful connection |
| `push_mode` | `on_save`, `interval`, `manual` |
| `interval_minutes` | Used by `interval` |
| `include_notes` | Whether research/notes are pushed |
| `enabled`, `last_push_at`, `last_commit_sha`, `last_error` | Status |
| `preset`, `username`, `author_name`, `author_email` | Built in migration 1200: how the form was filled, the user name for an HTTPS token, the optional commit identity |
| `state` | `new`, `synced`, `behind`, `rejected`, `blocked` |
| `last_attempt_at`, `next_attempt_at`, `local_sha`, `remote_sha`, `mirror_hash`, `last_error_code`, `fail_count`, `created_by` | Retry and divergence bookkeeping: the newest local commit, the remote head as last seen, the mirror tree the local head was built from |
| `created_at`, `updated_at` | |

Unique `(project_id, name)`. One in-flight push per remote enforced by a partial unique index on `git_pushes(remote_id) WHERE status='running'`.

Phase 7 (migration `1830`) adds four columns, none with a CHECK (the code validates them): `audience` (`private`: the full mirror; `public`: only what a reader of the book sees, see OPERATIONS "What a remote may carry"; rows that existed before the migration read as `private`, a remote made now is `public` unless its owner says otherwise, and an unknown value is read as `public`), `only_done` (a public remote carries only documents whose status is done), `sign_commits` (commits are signed with the project's signing key) and `created_via` (`publish` for a remote `repo.publish` made). Migration `1835` adds `restore_paused` (a restore set it; only the owner switching the remote on clears it). A project may have up to five remotes (`maxRemotesPerProject`); each has its own local repository, branch, folder, audience, state, last result and backoff.

### `git_public_addresses`

Migration `1836`. `project_id` (cascade), `address` (the normalised scheme, host, port and path), `since`; primary key `(project_id, address)`. The addresses the project has ever given a public remote. It outlives the remote: a remote made or changed to carry the full mirror at one of them needs a person at a browser.

### `git_accounts`

Migration `1831`. A person's connected account at a code host: `id` (`gac_…`), `user_id` (FK, cascade; the token belongs to this person alone), `provider` (`github`, `forgejo`), `host` (the web host, with a port when it is not 443), `login` (as the host reported it), `label`, `token_sealed` (the token, sealed with the instance key; the context names the row and the API address, so a copied or re-pointed row does not open), `created_at`, `last_used_at`. Unique `(user_id, provider, host, login)`; at most five per person. No op returns the token.

### `git_releases`

Migration `1832`. One attempt to publish an edition as a release asset: `id` (`grl_…`), `remote_id` and `project_id` (cascade), `tag`, `format` (`epub`, `docx`, `pdf`, `md`, `md_bundle`), `preset`, `status` (`running`, `ok`, `error`), `commit_sha` (what the tag points at), `input_hash` (the compile's manifest hash), `file_sha256`, `asset_name`, `asset_bytes`, `reused` (the asset was already at the host), `release_url` (built from the remote's address, never taken from the host's answer), `error_code`, `error_message`, `started_by`, `started_at`, `finished_at`. A partial unique index on `(remote_id, tag) WHERE status = 'running'` allows one attempt in flight per remote and tag. Only the newest 100 per remote are kept.

### `git_triggers` and `git_trigger_deliveries`

Migration `1833`. `git_triggers` is one row per remote (PK `remote_id`): `url` (scheme, host and port only), `path_sealed` (the path and query, which may be the receiver's secret), `secret_sealed` and `secret_hint` (the signing secret, shown once), `enabled`, `disabled_reason`, `last_status` (one short line: `delivered, HTTP 204`, `failed: timeout`; never a response body), `last_at`. `git_trigger_deliveries` is the outbox, one row per successful push: `push_id`, `commit_sha`, `branch`, `state` (`pending`, `delivered`, `dead`), `attempts` (capped at five), `next_attempt_at`, `last_status`, `finished_at`. The body of a delivery is built when it is sent (the project's slug, the repository's host and path, the branch, the commit id): nothing of it is stored.

### `git_signing_keys`

Migration `1834`. One row per project (PK `project_id`): `public_key` (an authorized_keys line), `fingerprint`, `key_sealed` (the ed25519 private key, sealed; never returned), `created_by`, `created_at`. It is the project's, not a remote's, so one allowed-signers line verifies the commits of every remote that signs.

### `git_scan_acks`

Migration `1210`. `remote_id`, `path`, `sha256`, `rule`, `acked_by`, `acked_at`; primary key `(remote_id, path, sha256)`. One acknowledged finding of the pre-push check: a file and the SHA-256 of the content that was looked at. A changed file does not match and is checked again. Each acknowledgement is also in the audit log.

### `git_pushes`

`id`, `remote_id` (and `project_id`, denormalized), `trigger` (`save`, `interval`, `manual`, `retry`, `force`), `outcome`, `forced`, `remote_sha` (the remote head before the push), `error_message`, `detail_json` (the changed documents; for a refused push the remote's commits that are not ours; for a force push the bundle), `status` (`running`, `ok`, `rejected`, `error`, `cancelled`), `started_at`, `finished_at`, `commit_sha`, `parent_sha`, `branch`, `files_changed`, `bytes`, `error_code`, `manifest_json` (path → hash of what was sent).

### `project_sources`

`id`, `project_id`, `kind` (`git`, `epub`, `docx`, `pdf`, `markdown`), `source_url`, `source_path`, `source_hash`, `commit_sha`, `metadata_json`, `kept_copy_path`, `imported_at`.

A Word document import records kind `docx`, with `source_hash` the SHA-256 of the file itself and the preview token in `metadata_json`; an Obsidian vault import is kind `markdown` with `layout: obsidian` in `metadata_json`. A Scrivener, WordPress or Ghost import is kind `markdown` too (the `kind` list is a CHECK constraint, and changing one means rebuilding the table), with `layout` (`scrivener`, `wordpress`, `ghost`), the hash of the source (the binder, or the export file) as `source_hash`, and under the layout's own name a report of counts (what was read and not kept: labels, keywords, comments, categories, attachment addresses, ...). A document made from one has a `document_sources` row whose `source_path` is where it was in the source (the binder path `Draft/Part One/The Road/Dusk`, or `wordpress/post/<slug>`, `ghost/post/<slug>`); a WordPress or Ghost post's date is its `story_date`.

A project can have more than one source (a Git import and a later EPUB edition); the most recent is the active comparison base. For a Markdown folder import (migration `1300`), `source_path` is the folder's base name (never a path on the caller's machine), `source_hash` is the preview token (a hash over every accepted file's path and content), and `metadata_json` records the layout, the manifest file, the scene-splitting option and the counts. No copy of the source is kept.

### `editions`

Migration `1800`. The provenance of an imported book (F-372): `id` (`edn_…`), `project_id` (cascade), `source_id` (the `project_sources` record of the import, `ON DELETE SET NULL`; NULL for an edition registered without one), `kind` (`epub`, `docx`, `pdf`, `markdown`; only `epub` is built), `label` (what a person calls it; the title and date unless named), `identifier` (the book's own: an ISBN or `urn:uuid`), `sha256` and `size` of the source file, `has_file`, `metadata_json` (what the book says about itself and counts of what the import read and did not keep), `imported_by`, `imported_at`.

The source file is kept intact at `files/projects/<project id>/editions/<sha256>.<ext>`: beside the attachments, not among them, so it is not linked from text, is not served to readers, and is never copied into the mirror, a Git push or an export (those carry the edition's metadata only). It is removed with the project (the purge removes the project's folder), kept with it in the trash, and travels with a backup because the rows and the `files/` tree do. Two triggers keep it in `file_usage` (the project, the person, the instance: the quotas attachments use); the same bytes held by two editions of one project are one file, counted once. At most 50 editions a project, 32 MB a file.

`edition_chapters` holds the book as read, in reading order: `(edition_id, ord)` primary key, `parent_ord` (0 at the top), `document_id` (the document the chapter became; NULL when the writer left it out or the document is gone; several chapters name one document after a merge, and a chapter split into scenes names the chapter's document), `source_path` (the file in the book, with `#fragment` when a file holds more than one chapter), `kind`, `title`, `words`, `sha256` and `baseline_gz` (the Markdown the converter made, deflated, **before** the writer's edits; a picture is `scriobh-picture:<sha256 of its cleaned bytes>` so a baseline reads the same in every project). Together with `document_sources` (the per-document path after the edits) it is what a later diff maps chapters by. `domain/editions` is the Go surface (`List`, `Get`, `Chapters`, `Baseline`, `ForDocument`, `Insert`, `Stage`, `Open`); see `docs/p7-notes/epub.md`.

The edition diff (F-373, F-377, `docs/p7-notes/editiondiff.md`) adds no table: it reads `editions`, `edition_chapters` (the recorded `document_id` and `source_path`, and the baselines), `document_sources` and the project's documents, and writes only through the document and review ops (a suggestion, or a restore-style revision).

### `import_jobs`

`id`, `project_id` NULL until created, `kind`, `source`, `status` (`preview`, `running`, `done`, `failed`), `mapping_json`, `report_json` (skipped files, warnings, word counts), `created_at`, `finished_at`, `error`.

The Markdown folder import is stateless, so it needs no job row. The repository import keeps a clone between preview and apply, and migration `1310` creates the table: `id`, `owner_id` (only the owner sees a job), `project_id` (NULL until applied), `kind` (`git`), `source_url`, `branch`, `commit_sha`, `status`, `token` (the preview token), `clone_path` (relative to the data directory), `report_json` (folder, name, summary), `created_at`, `finished_at`, `error`. A job that is never applied is removed after a day. Migration `1311` adds `applying` (a compare-and-set mark, so two requests cannot apply one job twice; the status list is closed and `running` means cloning).

### `document_sources`

`document_id` (PK), `source_id` (the `project_sources` row; the plan called it `import_id`), `source_path` (the file the document came from; a scene split from a file names that file), `source_order` (the node's position in reading order at import), `source_hash` (sha256 of the file's bytes as read, before line endings were normalised). Used to re-import and to diff an imported edition against the current manuscript.

---

## Alternatives, ghosts, stash

Built in Phase 3.5 (migrations `1400`, `1410`, `1420`). The tables below are canonical; the trailing `<!-- scriobh:alts {...} -->` line of `documents.content` is their serialisation, written by the server in the same transaction. The invariant, the comment format and the re-anchoring rules are in [OPEN-QUESTIONS](OPEN-QUESTIONS.md#phase-35-alternatives-decisions).

### `alternatives`

`id`, `document_id` and `project_id` (both cascade), `scope` (`word`, `sentence`, `paragraph`), `status` (`active`, `orphaned`), `anchor_start` and `anchor_end` (UTF-16 code units over the body: the text before the comment, line endings as LF), `anchor_prefix` and `anchor_suffix` (up to 24 characters of Markdown either side), `anchor_hash` (sha256 of the chosen text), `chosen_variant_id` (no foreign key: it names a child row; the save path keeps it true), `created_by`, `created_at`, `updated_at`.

The document body always contains the **chosen** variant, so search, word count, export and the mirror all read ordinary text, and `body[anchor_start:anchor_end]` is the chosen variant's text for every active alternative. An orphaned one keeps its last anchor, which is not trusted. There is no `resolved` status: resolving an alternative (`alternative.delete`) deletes the rows and keeps the earlier text, comment included, in a revision.

### `alternative_variants`

`id`, `alternative_id` (cascade), `text` (Markdown source), `source` (`manual`, `generated`), `is_original`, `position` (REAL, in cycle order), `author_id`, `created_at`.

Exactly one variant is `is_original` (a unique index), and its text is immutable (a trigger refuses the update; the ops refuse before that). `generated` is text suggested by a configured provider (Phase 4); nothing writes it yet.

### `ghosts`

`id`, `document_id` and `project_id` (cascade), `anchor_start` and `anchor_end` (UTF-16 units of the ghosted text, between the tags), `text_hash`, `preview` (the first 160 characters), `word_count` (the words the count leaves out), `created_by`, `created_at`.

A derived index, rebuilt from the Markdown on every save (the paired `<scriobh-ghost>` tags are the truth). It exists to give a ghost an id that survives saves, matched by the hash of its text, and to say who ghosted it and when.

### `document_annotations`

Phase 4 (Lab marks, masks and trims); not created. Anchors there use the same rule as alternatives: re-anchored on every save, and `stale` rather than moved to the wrong sentence.

### `stash_items`

`id`, `project_id` (cascade), `source_document_id` (set null when the document goes), `kind` (`paragraph`, `note`, `outline`), `title`, `content` (Markdown), `tags` (a JSON array, empty for none), `position` (REAL), `revision` (the concurrency token), `word_count`, `content_hash`, `created_by`, `created_at`, `updated_at`, `deleted_at`.

`stash_text` (`id`, `item_id` unique and cascade, `project_id`, `title`, `tags`, `body`) holds the plain text, written in the same transaction, with the external-content FTS5 table `stash_fts` and its triggers (migration `1420`). The stash is not part of any document, count, outline, export or the manuscript search, is part of a backup, and is written to `stash/` in the mirror (never in the manifest, so never pushed). Only editors and owners reach it.

---

## Search

```sql
CREATE VIRTUAL TABLE documents_fts USING fts5(
  title, synopsis, body,
  content='documents', content_rowid='rowid',
  tokenize='unicode61 remove_diacritics 2'
);
```

Triggers keep it in sync on insert/update/delete of `documents`. `body` is the plain text of the Markdown (syntax stripped, wiki links expanded). Entities have their own pair, `entity_text` and `entity_fts` (above); `comments_fts` is later.

Rebuild command: `scriobh search rebuild`. If the index is corrupt, the app degrades to `LIKE` search with a banner rather than failing.

---

## Semantic index (optional)

Migration `1890`. Off for every project until its owner confirms. The index is derived data and holds **no text**: offsets, a hash and a vector.

| Table | Holds |
| --- | --- |
| `embed_projects` | One row per project that ever switched it on: `enabled`, the provider (`provider_id`, no foreign key: deleting a provider pauses an index, it does not delete the owner's choice), `model`, `endpoint_key` (provider, normalised address, local flag and model, as confirmed), `allow_remote`, `enabled_by` (whose budget and personal provider it uses), the consent time and the counts the preview showed, `index_version`, `dim` (0 until the first vector), `gen` (bumped by every change to the chunks; keys the in-memory copy), `paused_reason` (empty, `restore` or `model_changed`), the last error class and the back-off (`fail_count`, `next_attempt_at`), `indexed_at`. |
| `embed_docs` | One row per document the worker has dealt with: the `content_hash` it embedded, the `model_key` (endpoint key) and `index_version` it was made under, `chunks`, and `status` (`ok`, `empty`, `failed`, `truncated`). A document with no text, or one the endpoint refused, has a row and no chunks, so it is not tried again until its text changes (a refused one is tried again after an hour). Cascades from `documents` and `projects`. |
| `embed_chunks` | One row per chunk: `ord`, `start_pos` and `end_pos` (characters into the plain text, counted as `search.query` counts an offset), `words`, `text_hash` (first 16 bytes of the chunk text's SHA-256, hex, to prove a snippet is what was embedded), `model`, `dim`, `vec` (`dim` signed bytes) and `mult`. Cascades from `documents` and `projects`, so a purge or a document's deletion takes its embeddings with it. |

A vector is stored as int8 with one multiplier: the vector is scaled so its largest number is 127 and rounded, and `mult = 1 / sqrt(sum(stored[i]^2))`, so `mult * sum(stored[i] * q[i])` with a unit query `q` is the cosine of the stored vector. It is a quarter of the size of float32: 768 dimensions are 768 bytes, so 10,000 chunks are 7.7 MB in the database, in a backup and in the memory a search holds (30.7 MB as float32).

---

## Sizing

A 150,000-word novel with 800 documents, 20,000 revisions, 400 entities, and 30,000 mentions is roughly:

| Data | Size |
| --- | --- |
| Text of the manuscript | ~1 MB |
| Revisions (pruned) | 40–120 MB |
| Entities and mentions | 10–20 MB |
| Events (90 days) | 20–50 MB |
| FTS index | 2–5× the text size |
| Total database | comfortably under 500 MB |

Backups are a zip of a `VACUUM INTO` copy of the database, the `files/`, `mirror/` and `conflicts/` trees, and a `manifest.json` listing every entry with its size and SHA-256, the migrations applied and the project and document counts. The secret key is never in it. Restore is `scriobh restore <archive>` into an empty data directory (or `--force`, after a pre-restore backup); it checks the manifest in both directions, migrates and integrity-checks the staged database, and compares the project and document counts with the manifest before it replaces anything. See `docs/OPERATIONS.md`.
