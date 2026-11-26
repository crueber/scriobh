# Plan: chapter breaks when importing a Word document, on a server-rendered review

Status: revised after the owner's answers; proposal for a builder, no code changed. Written against v2.3.0 (`81f5b6a9`).
Target path in the repo: `docs/plans/DOCX-IMPORT-CHAPTERS.md`.
Owner's request: "The import functionality really needs to be able to go through the existing document and set chapter breaks for importing on a DOCX."

Sample files and raw results:
- generator for the 22 small files: `internal/imports/testdata/docxcorpus/mk.py` (takes an output directory)
- generator for 150,000-word books: `internal/imports/testdata/docxcorpus/big.py`
- what the v2.3.0 importer does with each sample: `docs/plans/DOCX-IMPORT-CHAPTERS-baseline.txt`

## 0. What the owner decided

| # | Decision | Where it lands |
| --- | --- | --- |
| 1 | The import review is rebuilt in HTMX: server-rendered fragments, not a tree built in the browser from JSON. | Sections 5.1 to 5.5, phase 1 |
| 2 | The changed default is accepted: the heading level and parts are chosen automatically. | 5.6, decision D2 |
| 3 | Prologue and epilogue are chapters, not front or back matter. | 5.6, D3 |
| 4 | A contents page is left out by default, with "Put back". | 5.6, D4 |
| 5 | An ALL-CAPS title gets title case offered as a rename suggestion. | 5.7, D5 |
| 6 | A cut made only by page breaks (or section breaks, lone numerals, unformatted labels) is a suggestion, never applied automatically. | 5.6, 5.7, D6 |
| 7 | The original .docx is **not** kept. No edition. It is held temporarily while the writer reviews, and deleted. | 5.2, D7 |
| 8 | Word's automatic chapter numbers are left out of titles. | 5.6, D8 |

Because of 1, the staged upload comes first and is the foundation of everything else. Because of 7, "keep the original" is gone from the plan.

## 1. The problem, with numbers

Today a .docx is cut into chapters at Heading 1 and nowhere else. A manuscript that marks chapters any other way is not cut at all, and the whole book lands in one document.

I built 22 small .docx files by hand, one for each way writers mark chapters, and ran them through the current importer (a fresh build of `cmd/scriobh`, `scriobh import file.docx`, preview only):

| Sample | What the file has | What import does today |
| --- | --- | --- |
| 01 | Heading 1 chapters, `***` / `#` / centred `* * *` scene breaks | 3 chapters, scene breaks kept. Fine. |
| 02 | "Part One" as Heading 1, chapters as Heading 2 | 2 chapters named "Part One", "Part Two"; the real chapters are `##` headings inside. Fine only if the writer picks "Parts" in the select. |
| 03 | "Chapter One" as bold centred Normal text | **1 document, "Front Matter", whole book** |
| 04 | "CHAPTER ONE" then "THE ROAD" on the next line, plain | 1 document |
| 05 | lone roman numerals I, II, III, IV | 1 document |
| 06 / 06b | page break in a run before each chapter, with or without a label | 1 document |
| 07 / 07b | `pageBreakBefore` on the paragraph, or in a style | 1 document |
| 08 / 08b | next-page section breaks | 1 document |
| 09 | custom style "Chapter Title" with no outline level | 1 document |
| 09b / 09c | custom style based on Heading 1; direct outline level on a paragraph | 3 chapters. Fine. |
| 10 | Heading 1 with Word auto-numbering ("Chapter 1" is not in the text) | 3 chapters (titles without the number) |
| 11 | title page, copyright, dedication, contents, 2 chapters, acknowledgments, about the author | one "Front Matter" lump (copyright, dedication and the contents lines together), 2 chapters, 2 back-matter documents |
| 12 | no markers at all | 1 document |
| 14 | mixed: two Heading 1 chapters, two plain bold or caps "Chapter N" lines | 2 chapters; **chapters 2 and 4 are swallowed into the one before, silently** |
| 16 | book title as Heading 1, chapters as Heading 2 | 1 chapter named after the book; the 3 chapters are `##` headings (scenes only if "split at ##" is ticked) |
| 17 | Shunn-style: centred "Chapter One", space above, page break before | 1 document |

At book size (generated, 150,000 words, 60 chapters, bold centred "Chapter N", page break before each) the preview says `1 documents, 150120 words`, kind `front_matter`, with one warning, "this document will open read-only." That is invariant 13 broken by the importer. The folder importer also refuses a Markdown file over 2 MiB (about 350,000 words), which the unsplit path would reach for an omnibus.

The current behaviour is a decision (OPEN-QUESTIONS, "Importing a Word document": Heading 1 is a chapter). The gap is that nothing else is looked at and the writer is never told that nothing was found.

## 2. How import works today

All in `internal/imports`, `internal/server` and `web/static/js`. DOCX does **not** go through Pandoc.

- **Reader.** `docxread.go` opens the zip and reads `document.xml`, styles, numbering, footnotes, comments, relationships, core properties. `docxconv.go` (`docxToFiles`) walks the paragraphs once into a stream of `mdBlock` (paragraph, heading, scene, quote, code, list, table) carrying footnotes, tracked changes and comments. A paragraph is a heading if it has an outline level (direct, from its style, or from a style named "heading N"). Scene breaks: style "Scene Break", a bottom-border paragraph, a lone `#`, `* * *`, `***`, `---`, `⁂` and a few more (`reSceneText`), or two empty paragraphs in a row.
- **Cut.** `docxConv.files()` cuts the stream at the lowest heading level, or with `docx_headings: parts` at that level for parts and the next for chapters. Text before the first heading becomes `00-front-matter.md`. The output is a folder of Markdown files that the **folder importer** reads: preview, token, edits, caps, provenance. `files()` is also called by the PDF importer (`pdfconv.go`) and the Pandoc importer (`pandocimport.go`), so a better cut helps both.
- **What it ignores.** Page breaks (`w:br w:type="page"`), `w:pageBreakBefore` (direct or in a style), section breaks (`w:sectPr` in a paragraph), centring, font size, spacing, Word's automatic numbers, custom style names (apart from Scene Break, Title, Subtitle, Byline, quote and code styles), and all text. The only text rule is stripping "Chapter 3: " from a heading.
- **Preview and confirm exist.** `import.preview` writes nothing and returns the proposed tree and a token; `import.apply` writes in one transaction. The writer can rename, change kind, move, nest, join into another, leave out, and split at `##` headings (into scenes). There is no split at an arbitrary paragraph, no change of which paragraphs start chapters, and no "join with previous" shortcut.
- **How the page works, and why it is rebuilt.** `import.js`, `import-books.js`, `import-formats.js` and `import-repo.js` (about 24 KB gzip together; limits 8, 9, 3.5 and 4.5 KB) read the chosen file in the browser in 3 MiB slices, send it as base64 inside JSON on **every** preview, keep the edit list, order and left-out list in the browser, and build the tree from the JSON answer. The server holds nothing between calls. That is not HTMX, and it re-uploads and re-converts the whole file on every change (a PDF goes through its child-process limiter each time). The owner wants it server-rendered.
- **The page serves every format.** Markdown folder or zip, Obsidian vault, Word, EPUB, PDF, Pandoc formats (when an administrator switched the bridge on), Scrivener, WordPress, Ghost, and (on `/import/git`) a Git repository, each as a new project or added to an existing one (`/p/{slug}/import`).
- **Original file.** Only the SHA-256, size and name are recorded (`project_sources`, kind `docx`). An EPUB is kept as an edition (that stays as it is). A .docx is not kept, and after this plan it still is not (D7).
- **Limits in force.** Docx 34 MiB on the page, request body 48 MiB (`MaxBody`); 2 MiB per Markdown file; 2,000 files; 10,000 listed entries; 5,000 documents; `docs.WarnWords = 15,000` and `docs.ReadOnlyWords = 25,000`.
- **Other formats and chapters.** Markdown folder: files and folders; `split_scenes: h2`. EPUB: `epub_sections auto|toc|files|headings` (the model for a strategy choice with an automatic default). PDF: outline, else heading size. Pandoc formats: first heading level. F-344 (plain text, chapters by pattern) is an unbuilt P3 row. **No importer finds chapters from text.**
- **MCP.** The import ops are excluded from MCP by design ("import ops ... never are"); the new ops join that list with a written reason.

## 3. What Pandoc keeps (tested)

Pandoc 3.9 (the pypandoc-binary build in a scratchpad virtualenv; not Scríobh's admin bridge, which was not run), `-f docx -t gfm` and `-t native`, on the same files:

| Feature in the .docx | Pandoc |
| --- | --- |
| Heading 1 to 9, and a custom style based on Heading 1 | kept as `#` headings |
| Direct `w:outlineLvl` on a paragraph with no heading style (09c) | **lost** (Scríobh's reader keeps it) |
| Page break in a run | **dropped**, no trace in `native` |
| `w:pageBreakBefore`, direct or in a style | **dropped** |
| Section break (`w:sectPr` in a paragraph) | **dropped** |
| Centred, font size, space before | **dropped** (bold and italic kept) |
| Title and Subtitle styles | go to metadata, not the body |
| Automatic number ("Chapter 1" from the numbering definition) | **lost** |
| Custom paragraph style names | only with `-f docx+styles`, as a `custom-style` Div with the name; the style's properties are not carried |
| `***`, `#`, `* * *` lines | plain escaped text |

Pandoc cannot supply the main signals, and it is optional. **Do not route DOCX through Pandoc.** Extend Scríobh's own reader, which already sees all of these in `document.xml`. Pandoc-read formats (ODT, RTF) get the text rules only, since their block stream has no layout signals.

## 4. What real manuscripts use

Ordered by how far each can be trusted. A heading style is the writer's statement. A page break only shows where the page changed.

1. **Heading style.** Heading 1 or 2, an outline level, a style based on one. Vellum and Atticus tell writers to do this.
2. **A custom paragraph style as chapter title.** "Chapter Title", "Chapter Heading", "Chapter". No outline level.
3. **A label line.** "Chapter 3", "CHAPTER ONE", "Chapter Three: The Road", and Chapitre, Kapitel, Capítulo, Capitolo, Hoofdstuk, in any formatting; often bold, centred or large; often followed by a title line (sample 04).
4. **A lone number or roman numeral** (I, II, III; 1, 2, 3; "One") in a short centred or bold line.
5. **A page break before a short title-like paragraph.**
6. **A next-page section break.** Common in exports from Google Docs and Pages; weak.
7. **Parts.** "Part One", "Book Two", one level above the chapters or at the same level.
8. **Scene breaks.** `***`, `#`, `* * *`, `—`, ornaments, empty-paragraph gaps. **Never chapter candidates**, whatever their style.
9. **Front matter.** Title page, copyright ("©", "All rights reserved", ISBN), dedication, epigraph, contents (a TOC field, "TOC Heading" or "toc N" styles, dot leaders), foreword, preface.
10. **Back matter.** Acknowledgments, about the author, also by, glossary, appendix, afterword, author's note. (Prologue and epilogue are chapters: D3.)

## 5. Design

### 5.1 The shape

```
 browser                    server                                          disk / database
 ───────                    ──────                                          ───────────────
 choose file ──multipart──▶ import.stage (op)  ──────────────────────────▶  data/imports/stage/<id>/source + import_drafts row
 /import/d/<id> ◀─page───── review page, rendered from the draft
 click / change ─hx-post──▶ import.draft.update (op: options, edits, order)  draft row, revision + 1
              ◀─fragment─── review body re-rendered (tree, panel, notes)
 Import button ─hx-post──▶ import.apply / import.append (op, stage_id)  ─▶  one transaction; stage deleted; row kept as 'done' (counts only)
```

- The staged file and the writer's review state live **on the server**, keyed by a draft id and scoped to the person.
- Every request after staging is small: a form with a revision number and a key. The file is never re-sent.
- The converters, the plan, the folder importer, the token, the caps and the provenance stay as they are; they are called with the staged source instead of with bytes in the request. The inline paths of the four ops (files, zip, docx and the rest in the JSON body) **stay**, unchanged, for REST, the CLI and agents.
- The DOCX detector (5.6) goes in front of `files()`, in phase 2.

### 5.2 Staged upload

Staging is a write (it makes a file and a row), so it is an op with declared auth, covered by the authorization sweep, and generated into the skill and CLI by `make skill`.

**`import.stage`**: `POST /api/v1/imports/stage`, `RawBody` (the file's bytes are the body, streamed, never buffered whole: the pattern of the attachment upload and the cover form in `coverui.go`), `NoDryRun` (it writes outside the database), `Auth: ops.Writer()` (the same as `import.preview`).

| Query field | Meaning |
| --- | --- |
| `kind` | `docx`, `epub`, `pdf`, `zip`, `pandoc` (with `format`), `file` (a lone WordPress `.xml` or Ghost `.json`) |
| `name` | the name to show; cleaned like any title, 200 characters |
| `project`, `parent` | optional: stage for **adding to** this project (and into this folder of it). The caller must be able to edit that project, checked at every later call too |

- **What it does.** Streams the body to `data/imports/stage/<id>/source` with a size limit by kind (the existing ones: 34 MiB for a Word file or a zip, 32 MiB for a book), sniffs the first bytes against `kind` (a zip starts `PK`, a PDF `%PDF`; a mismatch is refused in plain words), runs the cheap structural checks already written (`checkZipDirectory` for a zip or .docx), computes the SHA-256, inserts the draft row, and answers `{stage_id, kind, name, bytes, sha256}`. A second stage of identical bytes by the same person for the same target returns the open draft instead of making another.
- **A folder of Markdown** has no single body. The page's folder form posts multipart parts (one per file, named by relative path); the UI handler (not the op) streams them through the intake's existing filters (hidden files, over 2 MiB, not Markdown, 10,000 listed entries, 32 MiB in all) into a zip it writes to a temp file, and hands that zip to `import.stage` with `kind=zip` and a flag `origin=folder`, so the preview's wording ("Hidden files and folders ... were left out") stays right. A few lines of glue script (5.4) keep the browser from uploading images: it sends only Markdown and manifests and lists the rest by name and size.
- **A Git repository** is already a server-side job (`import_jobs`, a clone in `data/imports/<job>/`). It is not staged again: its draft row points at the job (`source_kind = 'git_job'`) and the clone stays owned by `internal/gitmirror` with its own sweep.

**Deletion: on apply, on cancel, on expiry. Nothing else keeps the file.**
- *Apply:* after the transaction commits (or fails for good), the stage directory is removed. The row becomes `done` with the counts and the new project's id, and the row alone stays for 7 days so a reload of the page shows "Imported 41 documents" with a link. The row holds no manuscript text: ids, counts, names.
- *Cancel:* `import.draft.discard` removes the directory and the row. The page offers "Discard this import" and the chooser lists open imports with Resume and Discard.
- *Expiry:* a sweep (next to `sweepEditions` and `sweepImports` in `server.go`'s `sweepLoop`, plus once at start) removes any draft idle for 72 hours (`SCRIOBH_IMPORT_STAGE_HOURS`, default 72; 0 is refused), its directory and its cached conversion, and any `done` row older than 7 days. At start it also removes rows whose directory is gone (a restored backup) and directories with no row (a crash between the write and the insert).
- A failed or abandoned stream leaves nothing: the temp file is removed in a `defer`.

**Quota.** Reserved inside the insert transaction like `gitPreview` does: at most 5 open drafts a person (`SCRIOBH_IMPORT_STAGE_MAX_OPEN`), at most 256 MiB a person (`SCRIOBH_IMPORT_STAGE_USER_MB`) and 2 GiB in all (`SCRIOBH_IMPORT_STAGE_TOTAL_MB`), counting the source and the cached conversion (below); free disk checked as the Git clone does; each refusal says what to do ("Discard one of your open imports first"). These defaults are proposals. The attachment quota is not touched: a stage is not an attachment. Pictures and an EPUB's edition use their own quotas at apply, as now. The settings are listed in OPERATIONS.md and shown in the admin console like the Git ones.

**Where the file can and cannot reach.**
- `data/imports/stage/` is outside `files/`, `mirror/` and `conflicts/`, the only trees the backup archives (`internal/backup/archive.go`), so it is **never in a backup as content**. The database rows are archived; they hold the name, hash, options and the writer's edits (which can include chapter titles they typed), and no text of the book.
- It is never read by the mirror, the exports, the compile, the static site, the Git push, the search index or the diagnostics bundle (which says "no manuscript content": add a test that lists nothing under `stage/`).
- After a restore, drafts whose directory is missing are deleted by the start-up sweep.
- The storage report (F-332) lists the staged bytes as "Imports in progress" so an administrator can see and clear them.

**Authorization.** Every draft op takes the caller's id from the principal and loads the row `WHERE id = ? AND owner_id = ?`. Another person's draft, a missing one and a trashed target project are all **404**, so existence does not leak. Add every new op to the authorization sweep tests (`internal/server/authz_*_test.go`): signed out 401, reader role 403, a second writer 404, an API key pinned to another project 404 on a draft that targets a project, and a key pinned to a project cannot stage for a new project (as `import.preview` behaves today).

### 5.3 Where the writer's review edits live

**In the draft row, on the server.** One table, in the `1300` range ("project sources, import jobs, document provenance"; the next free number is `1320`): `internal/db/migrations/1320_import_drafts.sql`.

```
import_drafts(
  id           TEXT PRIMARY KEY,          -- imd_...
  owner_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_kind  TEXT NOT NULL CHECK (source_kind IN ('upload','git_job')),
  source_ref   TEXT NOT NULL DEFAULT '',  -- git job id; empty for an upload (its folder is the draft id)
  kind         TEXT NOT NULL,             -- docx, epub, pdf, zip, pandoc, file, folder, git
  name         TEXT NOT NULL DEFAULT '',
  target_project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,   -- NULL: a new project
  target_parent_id  TEXT,
  bytes        INTEGER NOT NULL DEFAULT 0,  -- the staged source plus the cached conversion, for the quota
  sha256       TEXT NOT NULL DEFAULT '',
  options_json TEXT NOT NULL DEFAULT '{}',  -- the op's own option fields: docx_split, docx_headings, docx_changes, split_scenes, epub_sections, pictures, layout, pandoc_format, ...
  edits_json   TEXT NOT NULL DEFAULT '{}',  -- edits by node key, order lists, the left-out list, project title/kind/author
  prev_json    TEXT NOT NULL DEFAULT '',    -- the state before the last change, for one Undo
  revision     INTEGER NOT NULL DEFAULT 1,
  token        TEXT NOT NULL DEFAULT '',    -- the preview token of the current state
  status       TEXT NOT NULL CHECK (status IN ('open','applying','done')),
  result_json  TEXT NOT NULL DEFAULT '{}',  -- after apply: counts and ids, no text
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX import_drafts_owner ON import_drafts(owner_id, updated_at);
```

IDs and times come from Go (`db.NewID`, `db.Now`). The file is never edited after it is applied; the next change to this table is `1321`. A cached conversion (below) lives next to the source on disk, not in the row.

**The ops.** Every change goes through an op (invariant 3); the UI handlers never write SQL.

| Op | Method and path | Auth | What it does |
| --- | --- | --- | --- |
| `import.stage` | `POST /api/v1/imports/stage` (raw body) | Writer | 5.2 |
| `import.draft.get` | `GET /api/v1/imports/drafts/{id}` | owner of the draft, else 404 | The draft, its options and edits, and with `preview=true` the preview (the same `PreviewOut`) |
| `import.draft.update` | `POST /api/v1/imports/drafts/{id}` | owner, else 404 | One change set: `base_revision`, then any of `options` (the format options and, in phase 2, `docx_split` and friends), `edit` (set or clear one edit by key: title, kind, skip, split, merge_into, parent), `reorder`, `project` (title, kind, author of the new project), `breaks` (phase 3), or `undo: true`. The change is **applied to a copy, the preview is run, and the draft is saved only if the preview accepts it**: a join into itself or a move into what is inside it is refused with the reason and nothing is stored (this replaces the browser's rollback to its "good" state). Answers the new revision and the preview |
| `import.draft.discard` | `DELETE /api/v1/imports/drafts/{id}` | owner, else 404 | Removes the row and the stage; not a deletion of anything imported |
| `import.preview`, `import.append_preview`, `import.apply`, `import.append` | as today | as today | Gain `stage_id`. With it, the source, the options and the edits come from the draft, and a request that also sends bytes or edits is refused ("send stage_id, or the files, not both"). `import.apply` also needs the draft's current `token` and `revision` |
| `import.git_preview` / `import.git_apply` | as today | as today | The Git review shares the shell: its draft row has `source_kind = 'git_job'`; apply dispatches on it |

All are excluded from MCP with written reasons in the snapshot (file upload; import). `make skill` regenerates the CLI and skill text; the committed snapshot must match (AGENTS.md). The option fields of the four preview and apply inputs repeat today; factor them into one embedded struct while adding `stage_id`, and check that `make skill` produces no other diff.

**Cached conversion.** A PDF, an EPUB, a Pandoc document and a big Word file are expensive to convert; edits do not change the conversion, only a change of a *cut option* does. The converted folder is cached under the stage directory, keyed by a hash of (source hash, cut options), reused by every preview of the draft, and deleted with it. The plan, the edits and the render are rebuilt per request from the cache. (For a 150,000-word .docx the conversion plus plan measured about 0.06 s through the CLI; the cache matters for PDF, EPUB and Pandoc.)

**Two tabs on one draft.** The draft is the single source of truth, so both tabs show the same state. Every form carries `rev`. A submit with an old revision is answered **409** `reason: revision` by the op; the handler re-renders the current state with a notice at the top ("This import was changed in another tab. This is the current version. Your change was not made.") and moves focus to the notice. Apply also carries `rev`, so a stale tab cannot apply what it is not looking at. Apply is claimed by a compare-and-set (`status 'open' to 'applying'`, as `import_jobs.applying` does), so two tabs or a double press make one project; a second press gets "This import is already being applied." `import.apply` takes an Idempotency-Key from the page (the draft id and revision).

**Reload and the back button.**
- The review page's address is `/import/d/{id}` (or `/p/{slug}/import/d/{id}`), rendered completely from the draft: a reload shows exactly the same review, with the same edits and the same open row. A reload with the network cut gets the PWA's offline page, not a broken one, and the draft is intact when the network returns (a gate in phase 1).
- Staging pushes that address into history (`hx-push-url`). Edits, rule changes, joins and splits do **not** (`hx-push-url="false"`), so Back never steps through edits. Back from the review returns to the chooser, which lists "Imports in progress" with Resume and Discard; nothing is lost and nothing is discarded silently.
- The review container carries `hx-history="false"`, and the page sets htmx's history cache to zero, so the manuscript's first lines are not copied into the browser's local storage by htmx's page snapshots. Add a test that checks local storage after a review.
- After Apply the address shows the finished result (from the `done` row) with links to the book; Back and reload stay safe.

### 5.4 The HTMX review shell

The Phase 7 screens already use this pattern (`hx-post` to a `/ui/...` handler that returns a fragment: the cover-spec form on the Compile page, the timeline check). The rebuilt import follows it.

**Pages and fragment routes** (all in `internal/server`, calling the ops above through `s.Reg.Call`; the page and fragments are templates with `.T` strings):

| Route | Returns |
| --- | --- |
| `GET /import`, `GET /p/{slug}/import` | the chooser (as now, one panel per format), plus "Imports in progress" |
| `POST /ui/import/stage` | multipart; streams to `import.stage`; on success `HX-Redirect` to the review page (a plain form post gets a 303 to it, so it works without script) |
| `GET /import/d/{id}`, `GET /p/{slug}/import/d/{id}` | the review page: heading, summary, notes, the "how it was cut" panel, the project fields, the tree, left-out list, skipped list, the Import button |
| `POST /ui/import/d/{id}/update` | one form action (below); returns the review body fragment (`#imp-review`) |
| `GET /ui/import/d/{id}/row?key=` | one row's edit panel, rendered when asked for |
| `GET /ui/import/d/{id}/more?after=` | the next 200 rows of a long tree |
| `POST /ui/import/d/{id}/apply`, `POST .../discard` | redirect to the book, or to the result; or the chooser |
| `GET /ui/import/d/{id}/progress` | for a Git clone or a slow conversion: a status fragment with `hx-trigger="every 2s"` until done (replaces `import-repo.js`'s polling) |

**Actions are plain forms.** Every control in a row is a real `<button name="act" value="up|down|top|bottom|skip|back|merge|parent|split|undo">` or a `<select>` or `<input>` inside a `<form hx-post=... hx-target="#imp-review" hx-swap="outerHTML">` with hidden `rev` and `key`. A title input and a kind select use `hx-trigger="change"`; buttons submit. The same forms work with no script (a 303 back to `/import/d/{id}#key`), which is also what the keyboard-only and screen-reader runs use. Joins and nesting use a `<select name="to">` beside a Join or Move here button inside the row's panel: no script-built picker.

**Rows are light.** A row is its title, kind, words, first line and warnings, and an Edit disclosure. The edit panel is fetched when opened (`hx-get .../row`), and rendered open for the row just acted on. 200 rows are rendered at a time with "Show more", so a 500- or 1,000-document book stays under the budget (below).

**Focus and announcements.** The server marks the element that should hold focus (`data-focus`) in the fragment; one small handler on `htmx:afterSwap` focuses it and scrolls it into view only if it is off screen. Announcements ("Moved The Road above The Mill: now 3 of 12", "Joined ...") are rendered by the server into a polite live region (`role="status"`) swapped out of band (`hx-swap-oob`), so the words come from the catalogue and the position from the server. Nothing animates or reflows on typing; there is no typing-driven request (inputs use `change`).

**Glue script.** One file replaces the four, `js/import.js`, with a limit of **2.5 KB gzip**, on the import pages only, doing three things: (1) the folder form's `htmx:configRequest` handler that sends only Markdown and manifests and lists everything else by name and size (a folder picker cannot be filtered without script; a zip works without it); (2) the focus handler above and `htmx:xhr:progress` for "Uploading 12 of 34 MiB"; (3) `hx-sync` is attribute-only. No framework, no client router, no state held in the page. `import-books.js`, `import-formats.js` and `import-repo.js` are **deleted**. The words they hold (`imp.js.*` in `internal/i18n/locales/en.json`, 132 keys) move to template strings under `import.*` and the old keys are removed; `i18n` tests that list used and unused keys are updated. Net: about 24 KB gzip of page script becomes at most 2.5 KB. The ARCHITECTURE budget row for the import page and the four limits in `web_test.go` (`internal/server/web_test.go`, lines for `js/import*.js`) are replaced by one.

**Styles.** `import.css` is kept and trimmed; check the per-page 25 KB limit (`TestCSSBudgetPerPage`) and run `scripts/check-contrast.py`. 390 px: rows are single-column; buttons are at least 44 px high and wrap; both themes use existing tokens; no gradients.

### 5.5 Every existing format must keep working

The shell is format-neutral: the draft's `kind` and `options_json` say what to convert, and the chooser's panels post their options with the file. Nothing is removed from the ops.

| Format | Staged as | Options kept in the draft | Review extras kept |
| --- | --- | --- | --- |
| Markdown folder | zip made from the folder form (`origin=folder`) | `split_scenes`, `layout` | split at `##`, rename, kind, move, join, leave out |
| Zip (Markdown, Scríobh export, or a Scrivener project or export the server detects) | `zip` | `layout`, `split_scenes`, Scrivener, WordPress and Ghost options | the detected kind named in the summary |
| Obsidian vault | `zip` or folder | `layout: obsidian` | the converter's report |
| Word | `docx` | `docx_headings`, `docx_changes`, (phase 2) `docx_split` ... | tracked-changes and comments panel |
| EPUB | `epub` | `epub_sections`, `epub_pages`, `use_cover`, `edition_label` | book panel, cut explanation, files list, nest and join, edition at apply |
| PDF | `pdf` | none beyond the common | fidelity panel, pages worth a look |
| Pandoc formats | `pandoc` + `format` | `docx_headings`, `pictures` | as Word |
| Scrivener, WordPress, Ghost | `zip` or `file` | `scrivener_*`, `wp_*`, `ghost_*`, `pictures` | research notes tree with Leave out and Put back, report |
| Git repository | server clone (`git_job`) | branch, subfolder (at the clone step) | the folder review |
| Adding to a project | any of the above with `project` (and `parent`) | same | "Add N documents", nothing existing changed |

**The browser scripts that must keep passing (all in `scripts/`), ported where they drive the old DOM:**

| Script | Drives | Port needed |
| --- | --- | --- |
| `e2e-import.mjs` | folder, zip and Word on `/import`, edits, apply, into an existing project, narrow screens (333 lines) | selectors (`#imp-...`) and waits change from "tree built by script" to "fragment swapped"; the old `#imp-h`, `[data-act]` hooks kept as the fragments' ids and attributes where possible, so most of it keeps working |
| `e2e-import-formats.mjs` | Scrivener, WordPress, Ghost panels, pictures, sheets (in `make e2e-fast`) | same |
| `e2e-import-epub.mjs` | EPUB panel, cuts, nest, join, edition, cover (380 lines) | same; the edition at apply is unchanged |
| `e2e-import-pdf.mjs` | PDF fidelity panel, tracked changes as suggestions (218 lines) | same |
| `e2e-importgit.mjs` | `/import/git`, clone, then the folder review | the review half moves to the shell; the clone and its progress follow `.../progress` |
| `e2e-pandoc.mjs` | the Pandoc panel on the import page and the compile side (329 lines) | the import half |
| `e2e-review-ui7.mjs` | a review of the import screens' defects: joins, undo, narrow screens | port; "undo" becomes the draft's Undo |
| `e2e-a11y.mjs` | the `import`, `import-pandoc`, `import-git`, `import-epub`, `import-epub-preview` and `import-pdf` page families in both themes at 1280 and 390 px | selectors; must still pass the audit |
| `e2e-editions.mjs`, `e2e-git.mjs` | reach `/import` and `/import/git` through links and the editions page | check links only |
| `e2e-alts.mjs`, `e2e-editions-diff.mjs`, `e2e-a11y.mjs` (its setup calls) | call the REST import ops with inline bytes | **unchanged**: those paths stay |

Go tests that name the page: `internal/server/importui_test.go`, `importrepo_test.go`, `import_test.go`, `import_pdf_test.go`, and `web_test.go` (the rendered-page list and the script limits). `scripts/csp-smoke.mjs` runs against the page (strict CSP, no outbound request): keep it green. Run `make e2e-fast` and `make e2e-extra` (the latter holds `e2e-import.mjs` and `e2e-importgit.mjs`).

### 5.6 Detection (phase 2)

Detection goes in front of `files()` and is a pure function from the block stream and options to a list of boundaries. It uses no clock, no randomness and no network, so the same file and options give the same cut (golden-tested). A cut at block level, before `compose()`, keeps footnotes renumbered per file and tracked-change suggestions and comments attached to their block.

**Signals recorded per top-level block** (the reader already parses these nodes; none is a new parse): style name and `basedOn` chain; outline level; `pageBreakBefore` (direct or from the style chain); `brBefore` (the previous paragraph is empty and holds only a page break, or this one starts with one); `sectBreakAfter` (a non-continuous `w:sectPr` in the paragraph); centred; all bold; all caps; size over the body median; space before; the number Word would print for a numbered heading (`numLabel`, shown in the review, **never put in the title**: D8); word count; ends with a full stop. `lastRenderedPageBreak` is ignored (a layout cache, stale in many files). Signals are read from the view the text is imported from (suggest, accept or reject), so a boundary never exists only in a view that was not imported.

**Step 1: what is not a candidate.** Scene-break markers (existing rules); a **contents region** (a TOC field, "TOC Heading" or "toc N" styles, or a "Contents" line followed by lines ending in dot leaders or a page number): those paragraphs never reach the label rule, so "Chapter 1 ........ 5" is not a chapter, and the region is left out by default (D4); paragraphs in tables, lists, quotes and code.

**Step 2: boundaries by category.** Categories decide; a score only labels.

*Strong boundaries* (cut automatically, all used together, so mixed files work: sample 14 gives four chapters and nothing is swallowed):
1. a paragraph with the chosen heading level, an outline level, or a style based on one;
2. a paragraph in a custom style whose name contains chapter, title, heading or part, used twice or more;
3. a **label line**: a chapter word (chapter, chapitre, kapitel, capítulo, capitolo, hoofdstuk; part or book for parts), then a number, roman numeral or number word, then nothing, or a separator and a title of up to 8 words, with at least one of: centred, all bold, all caps, larger than the body, a break before it;
4. **run promotion:** a label line with none of those signals (the first "Chapter 1" in sample 06 has nothing above it) joins the strong set when it belongs to a numbered run (1, 2, 3 in order, or two or more such lines) that already has strong members.

*Weak candidates* (**listed as suggestions, never cut automatically**: D6):
5. a page break before a paragraph that looks like a title (short, no full stop);
6. a next-page section break;
7. a lone number or roman numeral, centred or bold, in a run of three or more in sequence;
8. an unformatted label line outside a promoted run.

A weak rule cuts the book only when the writer chooses it (the radio "Page breaks (23)" in 5.7 is that choice; `docx_split=page_breaks` and the other explicit values do the same by REST). `auto` never uses them. Sanity checks on an explicit choice: more than 200 resulting chapters, or a median under 300 words in a document of 5,000 words or more, shows a warning ("these look like pages, not chapters") but does not refuse.

**Which heading level.** The lowest level that occurs at least twice. Two cases need no setting (D2):
- *Book title at Heading 1* (sample 16): Heading 1 occurs once, first, with no body text before it, and Heading 2 three or more times: Heading 1 is the project's title (not text) and Heading 2 the chapters.
- *Parts* (sample 02): the top level has two or more entries that are part labels and the next level carries chapters: parts and chapters with no setting. Part and chapter at the same level (Vellum style) also work: a part label starts a part and the chapters after it nest in it.

`docx_headings` (`chapters`, `parts`) stays as an explicit override.

**Titles.** A label with no title stays as the title ("Chapter One"). A label line followed by a title line (04, 17): the title is the second line and the label is dropped, as the existing "Chapter 3: The Road" convention does. A Word auto-number is not added to the title (D8). A boundary with no title text gets "Chapter N" with a "no title found" note.

**Title page and front matter.** The project title and author come from the Title style, else the file's core properties, as now. A first block that equals that title (case-folded) is the title page repeated and is dropped; any other text before the first chapter is front matter, split into documents rather than lumped: copyright (contains "all rights reserved", "©" or an ISBN), dedication (short, centred or italic, starts "For", "To" or "In memory of", or a Dedication style), epigraph, foreword, preface, each at a page break, section break or style change, named by what it is (the existing `frontMatterNames` plus those tests), else "Front Matter". **A contents page is left out by default** and listed under "Left out of the import" with Put back, as the PDF importer already does (D4).

**Back matter.** After the last chapter, headings or labels in the back-matter list (acknowledgments, about the author, also by, glossary, appendix, afterword, author's note) become `back_matter`. **Prologue and epilogue stay chapters** (D3): they are not in either list and no detector change makes them front or back matter. (The existing name lists, used for files in a folder import, already hold neither word; leave them.)

**Confidence.** A title score, 0 to 1, labels each boundary "sure" (0.6 or more) or "check", and orders the suggestion list. Starting weights, kept in one table in the code and tuned on the corpus and real files: heading or chapter-like style +0.55; label line +0.35; formatting (centred, bold, caps, size, space; at most +0.2 together); a break before +0.2; 12 words or fewer with no full stop +0.1; fits a run +0.1; ends with `.`, `?` or `!` -0.3 (a label excepted); 20 words or more -0.4. A heading of 13 words or more stays a boundary, marked "check".

**No strong boundaries found** (sample 12; a page-break-only book; a book with no marks): see 5.7.

### 5.7 The review: what the writer sees (phase 2 and 3)

The review body is one fragment; these parts are in it.

**"How it was cut" panel** (phase 2), above the tree: one sentence for what was chosen and why ("Cut at 23 Heading 1 headings." or "Cut at 23 lines that begin 'Chapter' (no heading styles were used; each has a page break before it)."), then the other ways as a radio group (a `<fieldset>` with real radios; arrow keys move; the change posts on `change`), each with its count and only the ones that exist:
- Chosen for you (the rule and its count);
- Heading 1 (n), Heading 2 (n), the style "Chapter Title" (n);
- Lines that begin "Chapter" or a chapter word (n);
- **Page breaks (n), Section breaks (n), Lone numbers (n)**: these are the weak rules; choosing one is the writer's explicit act and cuts;
- Pieces of about N words (n);
- No chapters, one document;
- (phase 3) Lines that match a pattern I type.

Changing the rule re-converts from the staged file, and the writer's edits are **kept for every chapter whose key still exists** (keys are stable, below) and **dropped, with a list, for those that do not** ("2 edits no longer apply: ..."), with an Undo button that restores the state before the change (`prev_json`). Nothing is discarded silently.

**Each row** shows, under the title: the first line in quotes (cut at 80 characters), the words, and a short reason ("Heading 1"; "Line begins 'Chapter', centred, page break before"). A boundary under 0.6 is marked "Check this one" with a border and text, not colour alone.

**A page-break-only book (and the other weak-only cases)** (D6). The review shows:
1. the plain message: "No chapter breaks were found by heading, style or 'Chapter' label. It would be imported as one document of 92,000 words.";
2. one line per weak rule that found something: "Found 23 page breaks in front of short lines. Found 22 next-page section breaks.";
3. under that, **"Possible chapter starts"**: a list (40 at a time, "Show more"), each a row with the line ("CHAPTER FOUR", 'By morning the snow had covered...'), why ("page break before; short; centred"), its position in words, and a button **"Start a chapter here"** (one press, keyboard reachable). Each press adds a boundary and splits the document in two on the next fragment;
4. a bulk button, **"Use all 23 page breaks"** (same as choosing the radio), and likewise for section breaks and lone numbers;
5. in a 15,000-word book or longer, **"Split into pieces of about 5,000 words"**, at the nearest scene break within 20%, else a paragraph end, titled "Section 1, 2, ..."; a suggestion too, not applied until pressed;
6. the Import button is disabled while any proposed document is 25,000 words or more (it would open read-only) unless the writer ticks "Import it as one read-only document anyway". The 2 MiB per-file limit still refuses what cannot fit, with its message.

Under 15,000 words with no strong boundaries the book simply imports as one document, and the suggestions are still listed.

**Titles in capitals (D5).** A chapter titled in all capitals gets, beside its title, "Use 'The Road'" (title case of the text, small words lower-cased as `prettify` does), one press, as a title edit; a panel button "Use title case for all N capitalised titles" does the lot. Nothing is changed unless pressed.

**Writer edits, phase 3.**
- **Join with previous**: a one-press button beside the existing "Join into..." (it is the existing `merge_into` edit aimed at the chapter above, so Put back and the footnote renumbering of `mergeBodies` work).
- **Split at a paragraph**: "Split this chapter..." opens the row's panel with a list of the chapter's paragraphs (first 80 characters each, 40 at a time), a `<fieldset>` of radios with Up and Down, Enter to choose (`hx-get .../row?split=1&after=` fetches pages of the list from the server: no chapter text is sent to the page whole). Choosing adds an explicit break at that paragraph's ordinal. Announced: "Split. Chapter 7 now ends '...'. The new chapter starts '...'."
- **Front and back matter**: the kind select exists; add "This and everything above is front matter" and "This and everything below is back matter".
- **Pattern rule**: a text field (starts with ..., or an RE2 expression, 200 characters at most, tested only against paragraphs of 200 characters or fewer; RE2 is linear time) and a Test button that shows the count and the first five matches before it is chosen.
- **Look inside a long chapter**: a chapter of 15,000 words or more (`docs.WarnWords`) shows the existing long-document note and a button, "Look for breaks inside it", which runs detection **inside that chapter** with the weak rules on and lists its own suggestions (chapters or, for a very long chapter, scenes at scene breaks). A chapter of 25,000 words or more (`docs.ReadOnlyWords`) blocks the Import button as in item 6 above.

**Stable keys.** A docx chapter's key is its file name `NN-slug.md`, numbered by position today, so adding or removing a boundary renumbers every key and every edit goes stale. Name the files `<ordinal, zero-padded>-<slug>.md` after the paragraph ordinal of the boundary, where the ordinal is the index of the paragraph among the body's `w:p` elements (the reader's `c.paragraphs` counter, which does not depend on the tracked-changes view). The folder importer still sorts them in reading order by the leading number. `reNumbered` reads `\d{1,6}` and the paragraph cap is 1,000,000, so the largest ordinal has seven digits: widen `reNumbered` to seven (a one-line change).

**Ops fields (phase 2 and 3).** Added to the option set that `import.draft.update` stores and the four preview and apply inputs accept, all optional, default `auto`:

| Field | Meaning |
| --- | --- |
| `docx_split` | `auto`, `heading1`, `heading2`, `style`, `label`, `page_breaks`, `section_breaks`, `numerals`, `size`, `none`; `pattern` in phase 3. `auto` uses strong boundaries only |
| `docx_split_style`, `docx_split_pattern`, `docx_split_words` | the style name, the pattern, the piece size (500 to 20,000, default 5,000) |
| `docx_breaks` | `{add: [ordinal...]}`: boundaries the writer added. Removing one is the existing join |

The preview answer gains `breaks` (`chosen`, `candidates` with counts, `suggestions` with ordinal, line, why, score, `none_found`) and per node `first_line`, `why`, `confidence`, `ordinal`, and `suggested_title`. `look` (the paragraph list for a split) is a read of the draft, `import.draft.get` with `look={key, after, count}`. `scriobh import book.docx` prints the same lines and the suggestions with their ordinals, with flags from `make skill` (`--docx-split`, `--docx-split-style`, `--docx-split-pattern`, `--docx-split-words`, `--docx-break N`).

### 5.8 Limits and performance

- **150,000 words and up.** Detection is one pass over blocks already in memory. A generated 150,000-word, 60-chapter, text-only .docx (177 KB) previews in about 0.06 s through the current `scriobh import`, which includes opening the database. Each review round trip rebuilds the plan from the cached conversion and renders 200 light rows.
- **Budgets to add to ARCHITECTURE.md** (targets until measured; the builder records the numbers, 11):

| Budget | Target |
| --- | --- |
| Stage a 34 MiB file on a LAN | streams; no whole-file buffer; memory growth under 8 MB |
| Review round trip (an edit, a join, a rule change that re-converts a .docx), 150k words, 60 chapters | < 150 ms p95 server time |
| The same at 500k words, 1,000 documents | < 400 ms p95 |
| First review page, 1,000 documents (first 200 rows) | < 400 ms |
| Import page script | `js/import.js` ≤ 2.5 KB gzip (replaces the 8, 9, 3.5 and 4.5 KB limits) |
| DOCX import, 150k words, stage to apply | < 5 s |
| EPUB import, 300 pages with pictures | < 60 s (the existing budget: the staged path must not slow it) |

- **Invariant 13.** The 15,000-word threshold in the plan today (`docs.WarnWords`) stays: a node at or over it gets the existing note and now the "Look for breaks inside it" button; at 25,000 (`docs.ReadOnlyWords`) a node blocks the Import button unless the writer confirms. The 2 MiB per-file limit is a hard stop.
- **Pattern safety.** RE2 only, 200 characters, short paragraphs only.
- **Existing caps** (5,000 documents, 2,000 files) still apply.
- **Disk.** The stage quota (5.2) bounds what drafts hold.

### 5.9 Data safety

- **The original file is not kept (D7).** Today only the SHA-256, size and name are recorded (`project_sources`, kind `docx`), and that stays. The staged copy exists only so the review can run: deleted on apply, cancel and expiry (5.2). The cut that was used (the rule and the boundary ordinals) is added to the provenance metadata (`docx.split`) so it can be explained later; the text of the file is not.
- **Idempotent or undoable.** Preview and every draft edit write nothing but the draft. Apply is claimed once (compare-and-set) and takes an Idempotency-Key (verified by a test, 11). A new project made by mistake is undone by the project trash, which exists. Adding to an existing project changes nothing that is there; phase 4 adds an "already imported" warning (the same SHA-256 already in `project_sources` for that project) and `import.undo` (trashes only the documents of that source that have not been edited since: content hash equals the `document_sources` hash). Until then undo is trashing those documents by hand.
- **Comments.** Block-level cuts keep each comment with its block. A comment on a heading or detected title is not importable today ("in a heading ... counted") and stays so, counted.
- **Footnotes.** Renumbered per file by `compose()` as now.
- **Images.** Still not imported (F-775; each is `[image]` and counted). An image-only paragraph is never a boundary.
- **Tracked changes.** Signals come from the view being imported; suggestions stay attached to their paragraph in whichever chapter it lands. A title with a tracked change is "taken as the reviewer left it", counted, as now.
- **Markdown stays canonical.** The output is the same ordinary Markdown as today: a `# Title` first line, blocks, footnote definitions, `* * *`. No new syntax and no metadata in the text. A title paragraph's bold or italic is removed (the title is plain text).
- **Untrusted content.** Titles, first lines and suggestions pass through `cleanLine`, `noMarks` and the converter's escaping; a title that holds `%%`, `[[` or a ghost tag cannot become one. The reasons shown are built from fixed words and counts; the quoted first line is set as text.

## 6. Decisions recorded

Put these in `docs/OPEN-QUESTIONS.md` under "DOCX chapter breaks and the staged import" when built.

- **D1. Server-rendered review.** The import review is HTMX fragments over a server-held draft. The browser holds no import state. The inline paths of the ops remain for REST, the CLI and agents.
- **D2. The default heading level and parts are automatic.** Changed output, on purpose: a book title at Heading 1 over Heading 2 chapters (sample 16) is now chapters, not one chapter; "Part One" headings over chapters (sample 02) are now parts without a setting; docx chapter keys are named by paragraph ordinal, not position. `docx_headings: chapters|parts` still overrides. Tests that pin the old behaviour (`TestDocxRoundTrip`, `TestDocxParts`, the DOCX goldens) change on purpose and each change is named in the release note.
- **D3. Prologue and epilogue are chapters.** The detector has no rule that makes them front or back matter, and the name lists do not hold them. The writer can change any chapter's kind in one select.
- **D4. The contents page is left out by default,** shown under "Left out of the import" with Put back, as in the PDF importer.
- **D5. All-capital titles keep their capitals;** title case is offered as a one-press rename suggestion, singly or for all.
- **D6. Page-break-only cuts are suggestions.** Page breaks, section breaks, lone numerals and unformatted labels never cut under `auto`. They are listed with "Start a chapter here" and a bulk button, and cut only when the writer chooses them. The size split for a book with no breaks is also a suggestion.
- **D7. The original .docx is not kept.** It is staged temporarily for the review and deleted on apply, cancel and expiry. It never enters a backup as content, the mirror, an export, the diagnostics or a Git push.
- **D8. Word's automatic chapter numbers stay out of titles.** Scríobh numbers chapters at compile; the number is shown in the review ("Word numbers this Chapter 3") and used as a label signal only.

## 7. Phases

Each phase ships on its own and has its own gates. A phase is not done until its gate list passes. The gates of earlier phases stay in force.

### Phase 1: staged upload and the HTMX review shell, every format still green

Scope: 5.2 to 5.5, no detector change. The Word import behaves exactly as today (one cut, at headings), now on the new shell.
- Migration `1320_import_drafts.sql`; `internal/imports` draft store (stage, get, update, discard, sweep, quota, recover); the four ops gain `stage_id`; the Git review joins the shell.
- The UI handlers, templates and fragments; `js/import.js` (≤ 2.5 KB); the three other import scripts deleted; catalogue keys moved.
- Config settings, OPERATIONS.md, admin "shown" list, storage report line, diagnostics test.

Gates:
1. `go vet ./...`, `go test ./...`; `go test ./internal/server -run Authz` with the new ops in the sweep (401, 403 reader, 404 other writer, 404 pinned key); `make skill` and the drift tests (`TestCommittedToolsAreCurrent`, `TestCommittedSkillIsCurrent`) pass with no unrelated diff.
2. Draft tests: stage, refuse a wrong kind and an oversized body, de-dupe identical bytes, quota refusals (open count, per-person, total), expiry sweep removes file and row, start-up recovery both ways, apply deletes the stage, discard deletes the stage, a failed apply keeps the draft usable, two tabs (stale `rev` gets 409 and a re-render), double apply makes one project, **`import.apply` honours an Idempotency-Key** (same key and body replays the first answer and writes once; a different body with the same key is refused), a restored backup drops drafts with no directory, nothing under `stage/` in a backup archive, the mirror, an export, the diagnostics bundle.
3. Every script in the 5.5 table passes (`make e2e-fast`, `make e2e-extra`), plus `scripts/csp-smoke.mjs`, `scripts/check-contrast.py`, `TestCSSBudgetPerPage`, and `web_test.go` with the one new script limit.
4. New browser checks (extend `e2e-import.mjs`): reload mid-review shows the same state; Back returns to the chooser and the draft is listed under "Imports in progress"; htmx stores nothing from the review in local storage; keyboard only from choosing a file to Import; 390 px; both themes; reload with the network cut shows the offline page and the draft survives; desktop smoke test (ephemeral loopback port, no proxy).
5. Bench: add `import.draft` cases to `internal/bench/run.go` beside `import.epub` (stage, 20 edits, apply) at 150k words; the numbers go in ARCHITECTURE.md's table, and the 150 ms review budget holds.

### Phase 2: the detector and the "how it was cut" panel (the biggest win for writers)
Scope: 5.6 and the first half of 5.7. Reader signals; `docxbreaks.go`; strong boundaries cut automatically; weak ones are listed; `docx_split`; `breaks`, per-node `first_line`, `why`, `confidence`, `ordinal`; stable keys; front and back matter by kind; contents left out; scene markers excluded; "No chapter breaks were found" plus the suggestion rows and bulk buttons (D6); the size suggestion; title-case suggestion (D5); the read-only gate.
Gates: the corpus and its expected splits (section 8) pass as golden tests; determinism test; the changed existing tests updated and named (D2); unit tests for each signal, tiering and the stable-key rule; the new e2e walk (open a page-break-only book, see the message and suggestions, press "Start a chapter here", use all page breaks, choose a different rule, Undo, import) with keyboard only at 390 px; `make skill`; bench `import.docx` at 150k words in three marker kinds and at 500k; FEATURES, ARCHITECTURE and OPEN-QUESTIONS changed.

### Phase 3: the writer's own edits
Scope: second half of 5.7: join with previous, split at a paragraph with the paragraph list, bulk front and back matter, the pattern rule, a look inside long chapters, `docx_breaks`.
Gates: unit and hostile tests for the pattern rule (long pattern, nested quantifiers on a long paragraph, many matches); a 5,000-boundary file does not exceed the document cap or the budget; stable keys survive adding and removing breaks; e2e for split-at-paragraph by keyboard at 390 px with the live-region words checked; both themes.

Built, with the differences recorded in [OPEN-QUESTIONS](../OPEN-QUESTIONS.md#docx-chapter-breaks-phase-3-decisions): the pattern has `docx_split_match` beside `docx_split_pattern` and is its own section with Test and Cut buttons (a radio once kept); Test is a save of the pattern that cuts nothing, not a read with the pattern in the address; the bulk "matter" and "within" are single change sets on `import.draft.update`; a join is tied to its target's text; an untitled chapter's key no longer follows its place; scenes found inside a long chapter start documents of the chapter kind.

### Phase 4: the rest
Scope, in this order, each its own change:
1. "Already imported" warning (same SHA-256 already imported into that project) and `import.undo`.
2. The detector for PDF, Pandoc formats and a plain-text importer (F-344), through `docxConv.files()`.
3. **The shared cut-safety function with `doc.split`, last, and only after the `split-chapter` branch has merged to main.** Nothing in `internal/domain/docs` is touched before then. That branch (commit `b4e5a382`, "editor: where a chapter may be cut, found through the serializer", and `16df9827`) works in the editor's JavaScript; `doc.split` in `docs.go` decides a cut's safety with `utf16Index` and `insideSourceBlock`. When it is merged, export one small function from `docs` and use it for the one case where the importer cuts finished Markdown: a split edit on a Markdown-folder file, a PDF or Pandoc result. The Word importer's own cuts are at block boundaries, safe by construction, and never needed it.
Gates: each item's tests; the undo test (edited documents are not trashed); the authorization sweep for `import.undo`; the existing `doc.split` tests unchanged; `go vet`, `go test ./...`.

Built, with the differences recorded in [OPEN-QUESTIONS](../OPEN-QUESTIONS.md#docx-chapter-breaks-phase-4-decisions): `document_sources.stored_hash` (migration `1321`) because the hash in `source_hash` is of the file and not of what was stored, and an undo also needs the document at its first revision; every document an import makes, parts included, has a `document_sources` row; `import.source.list` as the history; plain-text label lines are suggestions, as D6 says, so a text file opens with "one document, N places" and one press; a PDF or Pandoc document that has headings is cut as before and the detector runs for one that has none or when a rule is chosen; split at a paragraph, look inside and "within" stay Word-only; `docs.SafeCut` rather than one call per cut, and a heading under a line that cannot close a fence now stays text.

## 8. Tests: the corpus and expected splits

`internal/imports/testdata/docx/`: the 22 files from `tools/mk.py` (as Go builders beside `miniDocx` in `docx_test.go`, or as fixtures with the generator), each with a JSON of the expected rule, chapters (`kind`, title, first words, words), scene-break count and warnings. Under the design after the owner's answers:

| Sample | Expected under `auto` |
| --- | --- |
| 01 | 3 chapters (heading); project title from the Title style; `***`, `#`, `* * *` are scene breaks inside chapters |
| 02 | 2 parts with 2 chapters each, no setting |
| 03 | 3 chapters (bold centred labels are strong); the first "Sample Novel" line equals the core-properties title: title page, dropped |
| 04 | 3 chapters titled "The Road", "The Mill", "Snow"; each has a "Use 'The Road'" suggestion |
| 05 | **1 document**; "none found"; suggestion: 4 lone numerals (I to IV), bulk "Use all 4 lone numbers"; choosing it gives 4 chapters titled I to IV, "check" |
| 06 | 3 chapters (label plus page break; the first label promoted by its run) |
| 06b | **1 document**; "none found"; suggestions: 2 page breaks; "Use all 2 page breaks" gives 3 chapters titled "Chapter 1" to "Chapter 3" with "no title found" |
| 07 | **1 document**; suggestions: "The Mill" and "Snow" (the first title is the start); choosing the rule gives 3 chapters titled by their lines |
| 07b | **1 document**; one suggestion (a style with a page break) |
| 08 | **1 document**; suggestions: 2 section breaks |
| 08b | 3 chapters by label (bold labels are strong) |
| 09 | 3 chapters by custom style |
| 09b, 09c | 3 chapters (as today) |
| 10 | 3 chapters titled "The Road", "The Mill", "Snow"; the reason says Word numbers them "Chapter 1" and so on; titles unchanged (D8) |
| 11 | copyright (front matter), dedication (front matter), contents **left out** (Put back available), 2 chapters, 2 back-matter documents |
| 12 | 1 document, "none found", no suggestions |
| 13 | 1 chapter; 5 scene breaks (`***`, `#`, `* * *`, `—`, a double gap); 0 extra chapters |
| 14 | 4 chapters (headings, a bold label, an all-caps label) |
| 15 | 2 chapters; "Chapter 3 of the report was missing", a lone "I", "1." and "The End" are not boundaries and not suggestions |
| 16 | project title from Heading 1; 3 chapters from Heading 2 (D2) |
| 17 | 3 chapters titled "Chapter One", "Chapter Two", "Chapter Three" |
| prologue sample (new) | a "Prologue" and an "Epilogue" heading become chapters (D3) |

Also: the same files with a tracked change inside a title; generated 150k-word books for each of three markers and 500k (from `tools/big.py`); a false-positive set (a novel whose dialogue has lines "I" and "Chapter 3 of the report"); a file with a page break after every paragraph (5,000 suggestions: the list pages, the page does not hang, the explicit rule shows the "pages, not chapters" warning); hostile cases for the pattern rule; a "Chapter 1 ........ 5" contents region ahead of real chapters.

Real files, before the scoring weights are final: a set of real Word, Google Docs and Pages manuscripts (the owner's, with permission, or permissively licensed). None was opened for this plan.

## 9. Docs to change

`docs/FEATURES.md`: section Q and a new implementation-status section, in the existing `| ID | Feature | Pri | Phase |` format. `split-chapter` takes F-1224, so these start at **F-1230**. Update the text of F-341, F-708 and F-804 (the screens), and point F-344 at F-1244.

```
| F-1230 | Import takes a file once: `import.stage` holds it on the server, scoped to the person, while the writer reviews; deleted on apply, cancel and after 72 hours; never in a backup, the mirror, an export, or a Git push | P1 | 9 |
| F-1231 | The review state (options, edits, order, joins) is a server-held draft with a revision; two tabs, a reload and the back button are safe; one Undo | P1 | 9 |
| F-1232 | The import review is server-rendered HTMX fragments, works with no script apart from the folder picker, at 390 px and by keyboard; page script ≤ 2.5 KB | P1 | 9 |
| F-1233 | Every import format (Markdown folder, zip, Obsidian, Word, EPUB, PDF, Pandoc formats, Scrivener, WordPress, Ghost, Git repository, add to a project) uses the staged review | P1 | 9 |
| F-1234 | Word import finds chapter breaks by itself from headings (the lowest level used twice), outline levels, custom chapter styles, and formatted "Chapter N" lines (strong boundaries) | P1 | 9 |
| F-1235 | Parts and chapters are told apart without a setting; a book title at Heading 1 over Heading 2 chapters; Word's automatic numbers stay out of titles | P1 | 9 |
| F-1236 | Scene-break markers (`***`, `#`, centred `* * *`, ornaments, empty-paragraph gaps) are never chapters, whatever their style | P1 | 9 |
| F-1237 | Front matter is split by kind, a contents page is left out (Put back), back matter is named; prologue and epilogue are chapters; a title page becomes the project's title and author | P2 | 9 |
| F-1238 | The preview says how the book was cut and why, lists other ways with counts, and shows each chapter's first line, words, reason and confidence; changing the way keeps the writer's edits where the chapter still exists | P1 | 9 |
| F-1239 | A cut from page breaks, section breaks, lone numbers or unformatted labels alone is a suggestion: "Start a chapter here" rows and a bulk "use all" button; a book with no breaks says so and, from 15,000 words, suggests pieces of about 5,000 words | P1 | 9 |
| F-1240 | A chapter in all capitals offers its title in title case as a rename | P3 | 9 |
| F-1241 | The writer's own breaks: join with previous, split at a paragraph chosen from a list, bulk front and back matter, cut at lines matching a pattern, look for breaks inside a long chapter | P2 | 9 |
| F-1242 | Chapter keys are named by the paragraph they start at, so edits survive added or removed breaks | P1 | 9 |
| F-1243 | A file already imported into a project is flagged; an import into an existing project can be undone while its documents are unedited (`import.undo`) | P3 | 9 |
| F-1244 | The chapter detector is shared by the PDF, Pandoc and plain-text (F-344) importers | P3 | 9 |
| F-1245 | One cut-safety function shared by `doc.split` and the importer's Markdown splits | P3 | 9 |
```

Also: ARCHITECTURE.md ("Other sources" paragraph for Word, a "Staged imports" paragraph, the import page budget row replaced, the new budgets); OPERATIONS.md (the four `SCRIOBH_IMPORT_STAGE_*` settings, the sweep, the storage line); DATA-MODEL.md (`import_drafts`; the `1300` row of the ranges table adds `1320`); OPEN-QUESTIONS.md (section 6); docs/API.md and `skills/scriobh` (through `make skill`); the import page's hint text and the catalogue; the release note for the version that ships phase 2 (the repo has no CHANGELOG file; v2.3.0's notes are under `docs/`); README status when phase 2 ships.

## 10. How other tools do it

- **Scrivener** ("Import and Split"): splits a .docx at Heading styles (outline level) or at a text marker the writer types (`#` by default). No preview of the cut. Forum reports of titles dropped when page breaks sit in an unusual place. Not on iOS. ([L&L forum](https://forum.literatureandlatte.com/t/word-docx-import-and-split-by-outline-drops-document-titles-when-page-breaks-are-present/46977))
- **Vellum**: Heading 1 for chapters, reading "Chapter N: Title" (Chapitre, Kapitel, Capítulo recognised); Heading 1 "Part N" makes a part; Heading 1 matching Prologue, Acknowledgments and similar makes that element type; Heading 2 to 5 are subheads; centred `***` is an ornamental break; an empty line is a scene break; with no heading styles it "will do its best" and errors are fixed after import; title page and contents are to be removed before import. ([Vellum help](https://help.vellum.pub/importing/preparing/))
- **Atticus**: Heading 1 starts a chapter; stray page breaks create unwanted chapters, so remove them; `***` on its own line is a scene break; one .docx at a time. ([Atticus help](https://intercom.help/atticus-5877e36564df/en/articles/12622033-how-to-prepare-your-word-document-for-upload))
- **Plottr**: not checked.

All three rely on styles and fix mistakes after import. Here the writer sees and corrects the cut before anything is written, and text labels and page breaks help when no styles were used, as suggestions the writer accepts (D6).

## 11. Builder checks to carry out

1. **Idempotency.** Verify at runtime, with a test, that `import.apply` honours an Idempotency-Key. The registry applies keys to write ops unless an op is marked `Ephemeral`, and `import.apply` is not marked; this was read, not run.
2. **Measure and record.** Add `import.docx` and `import.draft` cases to `scriobh bench` (`internal/bench/run.go`, beside `import.epub` at line 698) for a 150k-word book in three forms (headings; bold centred labels with page breaks; no marks) and a 500k-word one; run them; write the measured numbers into ARCHITECTURE.md's budget tables next to the targets in 5.8, and fail the bench on the 20% regression rule like the other budgets. The only number measured so far is about 0.06 s for a 150k-word text-only .docx through the CLI, which includes opening the database.
3. **Option struct.** Factor the repeated option fields of `PreviewIn`, `AppendPreviewIn`, `ApplyIn` and `AppendIn` into one embedded struct and check `make skill` shows no other diff before adding `stage_id`.
4. **Folder form.** Confirm against `intake` that a zip written from the folder form with `origin=folder` produces the same wording and caps as the inline folder path today.
5. **`reNumbered` width** and the stable-key naming (5.7), with a test at the largest ordinal.
6. **Real files** (section 8) before the scoring weights are fixed.

## 12. Questions for the owner

None blocks phase 1. Two defaults I chose that the owner may want different:
1. **Draft retention and size.** 72 hours idle, 5 open drafts and 256 MiB per person, 2 GiB in all (5.2). A 150k-word book is small; the limits protect the disk. Change them if a longer review is normal.
2. **A folder needs a few lines of script.** A browser cannot filter a chosen folder without script, so a folder import (the picker only) uses the glue script to skip images; a zip works with none. If a folder must work with script switched off, the folder picker would upload every file, images included, up to the limit. Say if that is acceptable; the plan assumes the glue script.
