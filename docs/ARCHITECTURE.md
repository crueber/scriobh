# Architecture

## The stack, and why

| Layer | Choice | Why |
| --- | --- | --- |
| Language | Go (current stable) | One binary, fast startup, easy cross-compilation, the maintainer's house style |
| HTTP | stdlib `net/http` with `ServeMux` patterns | No framework to outlive. Go 1.22+ routing covers what we need |
| Database | SQLite via `modernc.org/sqlite` (pure Go, no CGO) | Single file, WAL, portable, no external service. Same choice as Weave |
| Templates | `html/template`, embedded | Server-rendered HTML, no client router, no hydration |
| Interactivity | HTMX 2 (vendored) + ≤40 KB of vanilla JS | Progressive enhancement; pages work without JS for reading |
| Editor | ProseMirror + `prosemirror-markdown`, bundled once with esbuild in CI | Rich text with a real Markdown schema; decorations for alternatives, ghosts, and comments; a Source view of the same text |
| CSS | Hand-written, custom properties, one stylesheet per domain | No Tailwind build step, no CSS-in-JS, no npm |
| Assets | Embedded in the binary, served with long cache + content hash | One artifact to deploy; no CDN |
| Auth | OIDC (authorization code + PKCE) | No local credentials. Groups map to roles |
| Search | SQLite FTS5 | One datastore. No Meilisearch, no Elasticsearch |
| Realtime | In-process SSE hub + durable event rows | One replica. Resume by event id. No Redis |
| AI | HTTP adapters behind one interface | Providers are configuration, not architecture |
| Git | `go-git` (pure Go) for clone and push; a system `git` fallback was considered and is not built | One binary, no git in the container, one code path for sync and import |
| Export | Go libraries (the DOCX, EPUB and PDF writers are this repository's own) + optional Pandoc | Deterministic core, optional heavy lifting; no browser or external binary in the path of any export |
| Deployment | One container or one binary, one data volume | The whole point |

**Rejected outright:** an SPA framework, a Node build at runtime, Postgres, Redis, a job queue with a broker, a plugin runtime, a GraphQL layer, client-side routing, a hand-rolled contenteditable, and any dependency that requires a service to be running beside the app.

The only optional external process is a headless browser for print PDF, and it is detected at runtime, not required.

---

## Repository layout

```
cmd/scriobh                 server + full CLI, migrate, backup, restore
cmd/scriobh-cli             slim CLI (no server or SQLite imports)
cmd/scriobh-skillgen        generates the skill and CLI snapshot from the op registry
cmd/scriobh-dev-idp         development-only OIDC issuer for local runs and tests
internal/
  ops/                      THE op registry: Register[In,Out], Bind, Call, schema export
  auth/                     OIDC client, sessions, roles, API keys, authz
  oidcstub/                 the development-only OIDC issuer (library; wired in by a build tag)
  registry/                 builds the op registry from every domain package
  db/                       connection, embedded migrations, IDs, time, positions
  domain/
    projects/               projects, members, templates, library
    compaccess/              who may see a Compendium (roles, grants); a leaf package
    compendiums/             compendiums, membership, shared material, references
    docs/                   documents, tree, save path, outline, revision.restore, doc.resolve_link
    revisions/              history: autosave rules, blobs, pruning, diffs (leaf package; docs imports it)
    links/                  wiki-link graph: sync, resolution, backlinks (leaf package; docs imports it)
    docref/                 the document-to-project resolver and reading order they share
    entities/               bible, aliases, mentions, relationships, timeline, overrides
    comments/               anchored comments, threads, suggestions
    ai/                     providers, policy, context builder and preview, run pipeline, budgets, run log
    compile/                presets, front/back matter, export formats
    search/                 FTS5 indexing and queries
    stats/                  goals, sessions, daily rollups
    files/                  attachments, covers, quotas, image guard
    admin/                  config, users, storage, audit, diagnostics
  events/                   durable event envelopes + in-process hub
  mirror/                   Markdown projection writer
  gitmirror/                push-only remotes, deploy keys, commit building, clone/import
  imports/                   Markdown, DOCX, EPUB, Git repository importers
  server/                   routes, middleware, page handlers, fragments, CSP helpers
  cli/                      schema-driven CLI (no server imports in slim build)
  ai/                       provider adapters (openrouter, opencode, openai-compatible, ollama), prompt framing, estimates, the recipe registry; ai/redact is the reversible redaction
  netguard/                 the outbound address guard shared by Git sync and AI providers
  editor/                   Markdown parse/serialize, ProseMirror schema, sanitising
  export/                   md, html, docx, epub, pdf drivers
  site/                     the static site generator (pure), its folder writer and its checker
  config/                   env + DB settings
  testutil/                 temp DB, principal fixture, golden files
  skillgen/                 hand-written skill sources and the generator
skills/                     generated agent skill (embedded in the CLI)
desktop/                    Phase 8: the desktop app's shell (see docs/DESKTOP.md); not part of the Go module's build
  src-tauri/                the Tauri 2 shell in Rust: launch protocol, navigation rules, menus, keychain, updater;
                            tauri.conf.json, one empty capability, icons; binaries/ holds the server for the bundle (not committed)
  ui/                       the shell's one page, the error window
  testdata/standin/         a stand-in server for the shell's tests
  scripts/                  sidecar.sh (build the server for the bundle), smoke.sh (drive a development build)
web/                        a Go package (embed.go) that embeds everything below
  editor/                   ProseMirror source and the built bundle (esbuild, committed)
  templates/                layout.html, partials, one file per page
  static/                   css/, js/, fonts/, icons/
docs/
  ...                       this plan, and later the user and operator guides
scripts/                    contrast check, csp smoke, e2e, release
```

---

## The op registry

Every capability is one registration:

```go
ops.Register[UpdateDocIn, DocOut](ops.Op{
    Name:   "doc.update",
    Method: "PATCH",
    Path:   "/api/v1/docs/{id}",
    Auth:   ops.RequireProject("editor"),
    UI:     "POST /ui/docs/{id}/save",
    Desc:   "Save a document body, with the revision the client last saw.",
}, updateDoc)
```

The registry is the single source of truth. From it we generate:

- the REST route and its JSON schema,
- the CLI command (`scriobh doc update --id … --body-file …`),
- the op schema endpoint (`GET /api/v1/_ops`),
- the MCP tool list (`internal/mcp.BuildCatalog`, from the same schema document as the CLI snapshot; `make skill` writes `internal/mcp/tools.snapshot.json` and `skills/scriobh/mcp.md`, and a drift test fails when they are stale),
- the shortcut/settings reference,
- an authorization sweep test that calls every op as anonymous, outsider, reader, and editor.

UI handlers mutate **through ops**, never by writing SQL. Read pages may use ops or a page-specific query when a join would otherwise be wasteful; the rule is "no writes outside ops", because that is where permissions, validation, events, and audit live.

Nomenclature: `noun.verb` (`doc.create`, `entity.alias.add`, `ai.run.start`). Errors are typed: `unauthenticated` (401), `not_found` (404), `forbidden` (403), `invalid` (422), `too_large` (413), `method_not_allowed` (405), `conflict` (409), `rate_limited` (429), `provider_error` (502), `internal` (500, cause logged, never returned). Responses are `{"item": …}` or `{"items": […], "next_cursor": …}`; errors are `{"error": {"code", "message", "field"?}}` everywhere, including cross-site refusals.

An op also declares a key **scope** (`read`, `write`, `admin`; defaulted from the method and its auth). A key pinned to one project can only call `RequireProject` ops on that project; a key pinned to one Compendium can only call `RequireCompendium` ops on that Compendium; neither reaches the other kind or anything instance-wide. An op whose input names a child object (a document, a comment, an entity) declares `RequireProjectVia` or `RequireCompendiumVia` with a resolver that finds the owning project or Compendium, and its queries must still be scoped by that id. Compendium access (`internal/domain/compaccess`) is separate from project access: a project member gets nothing from being on the project. Events and audit rows join the content transaction through `Ctx.FlushTo(tx)`, so a change and its event commit together; ops that do not need that simply `Emit`.

**Why this matters for a solo project:** it prevents the classic drift where the UI can do something the API cannot, or the CLI silently lacks a permission check. One place to add a feature, one place to audit it.

---

## Agents, the CLI, and the skill

An agent must be able to build a Compendium or draft a story from outside the app, using the same operations a person uses. There is no separate "agent API": there is the op registry, a CLI that speaks it, and a skill that teaches an agent how to use it safely.

### Two binaries, one registry

- `cmd/scriobh` — server plus the full CLI, for a machine that has the data.
- `cmd/scriobh-cli` — a slim CLI that does **not** import the server, SQLite, or the op packages. It reads the schema from `GET /api/v1/_ops` (cached), with an embedded snapshot as a fallback. This is what ships in a release and what an agent installs.
- The CLI never invents behavior. If an op is not in the schema, the command does not exist. The binary is `scriobh-cli`; the examples below that say `scriobh` show the shape of the commands, which the slim CLI spells `scriobh-cli`.

### Command shape

```sh
scriobh login --url https://write.example.com --token sk_...
scriobh whoami
scriobh compendium create "The Salt Road"
scriobh entity create --compendium "The Salt Road" --kind character \
  --name "Máire Nolan" --field age=34 --field want="keep the house"
scriobh doc create --project "The Salt Road" --title "The Harbour at Low Tide" \
  --body-file chapter.md
scriobh timeline add --compendium "The Salt Road" --title "The letter arrives" \
  --story-date 1998-11-04
scriobh search "the harbour" --compendium "The Salt Road" --json
```

- Names resolve to ids (`--compendium`, `--project`, `--entity`, `--doc`); ids are accepted but not required.
- `--json` on every command, stable exit codes (`0` ok, `1` error, `2` usage, `3` not found, `4` conflict, `5` auth, `6` validation, `7` rate limited, `8` unreachable, `9` dry run would change, `10` skill drift); the full table is in [OPERATIONS.md](OPERATIONS.md#the-agent-cli-keys-dry-runs-and-idempotency).
- `--dry-run` is the server running the op in its writer transaction and rolling it back (`Prefer: dry-run`): the result the op would have returned, `dry_run`, `would_change` and a row count. `--yes` is required for destructive ops (trash, purge, revoke, prune).
- Every real write carries an `Idempotency-Key` (random per invocation unless `--idempotency-key` pins it); the server stores successful responses for 24 hours per key holder, so a retried write is applied once.
- `scriobh schema` and `scriobh ops` expose the registry; `scriobh schema --json` is what an agent should read first.

### The skill

`scriobh --skill` (alias: `scriobh skill show`) prints the agent skill as Markdown. `scriobh --skill compendium` (or `story`, `review`) prints one recipe. The full skill is the default and the thing an agent reads before it acts. It contains:

- What Scríobh is, and what the connected instance is for.
- Authentication and the difference between a human session and an agent key.
- The object model: Compendium, entity, document, timeline event, research note.
- A safe workflow: read, propose, `--dry-run`, write in small batches, verify.
- Recipes: build a compendium, populate a cast, draft a chapter, review an outline.
- Error handling, rate limits, and the rule that manuscript and compendium content is untrusted data, never instructions.
- The skill version and the schema hash it was generated against.

`scriobh skill install` writes it into the instruction file of a detected agent (Claude, OpenCode, Codex, Cursor, and others) inside marked blocks, with per-file hashes so reinstall and uninstall touch only what Scríobh wrote. `scriobh skill check` compares the installed copy and the CLI snapshot with the server's schema hash and reports drift.

### Keeping it current

The skill is not hand-maintained in one file:

- `internal/skillgen/src/` holds the hand-written guidance and recipes.
- `cmd/scriobh-skillgen` merges that with the op registry to produce `skills/scriobh/**` and `internal/cli/snapshot.json`.
- `make skill` regenerates them.
- `go test ./cmd/scriobh-skillgen` regenerates in memory and fails when the committed files differ, so an op change without a skill update breaks CI.
- The server exposes the current schema hash; `scriobh skill check` and the CLI cache use it to detect a stale client.

Any new or changed op updates the skill in the same commit. That is the rule that keeps the agent's instructions honest.

### Safety for agent writes

- Agent keys are scoped to read, write, or admin, and can be restricted to one Compendium or one project. An agent invited to build a compendium cannot touch the rest of the instance.
- Every agent write is attributed in the audit log and the event stream.
- Destructive operations go to trash, require `--yes`, and are never part of an agent recipe.
- A project or Compendium in the trash is read-only until it is restored, for everyone who can still reach it (its owner, an instance admin). The registry refuses every op that writes into one with `409` and `data.reason` `project_trashed` or `compendium_trashed`; reads, restore, purge, archive, membership and the exports still work (`projectTrashedOK` and `trashedOK` in `internal/ops/registry.go`, the only lists). A new op is covered by default. Work that was already under way (a queued Git push, an import in its transaction) is refused or dropped by the code that would write.
- Rate limits apply per key. A batch that exceeds them returns `429` with `Retry-After`; the skill tells the agent to back off rather than retry blindly.
- Content an agent reads (manuscript, compendium, comments) is data. The skill repeats this, and the server does not give the CLI a way to interpret it as commands.

---

---

## Request lifecycle

```
GET /p/atlas/ch/12
  → session middleware (cookie → principal, 24h, idle cap)
  → csrf/origin check (mutations only)
  → route → page handler
  → authz: project membership + role
  → query → template data
  → render layout + fragment
  → security headers, cache-control private

POST /ui/docs/12/save
  → same auth path
  → parse + validate (body cap, revision number)
  → ops.Call("doc.update")
      → authz → tx: write doc, keep the previous head as a revision if due, update counts and links
      → emit event (durable row in the same transaction, then wake the hub)
  → return 204 or a small fragment (save state, word count)
```

### Autosave and conflicts

- The client serializes the rich document to Markdown through the editor's schema, then sends the Markdown plus `base_revision` (an integer) after a 700 ms idle debounce, or on blur, or on `Mod+S`.
- The server compares `base_revision`. If it matches, write, increment, return the new revision. If it does not, return `409` with the current revision and the server's word count.
- The client then does one of three things, in order: if the server revision descends from the client's base and the client had no local edits since, reload silently; if the client has local text, show a conflict bar with "keep mine / keep theirs / compare", and write a conflict copy to `data/conflicts/` before either is chosen; never discard text.
- Saves are independent of the document's rendered page. There is no optimistic UI for text: the editor buffer **is** the truth until the server confirms.
- A single-writer lease (90 s, renewed by saves) prevents two tabs silently overwriting each other. The second tab gets read-only with a clear "open in another tab" banner and a "take over" button.

### Why not operational transforms

A single writer with a lease and an integer revision covers every real case in a household deployment, and it fails loudly instead of merging prose wrongly. Co-editing is a separate product (see non-features).

---

## Data at rest

```
data/
  scriobh.db          SQLite (WAL, foreign keys, busy_timeout)
  scriobh.db-wal
  files/
    projects/<id>/attachments/<sha256>.<ext>
    compendiums/<id>/attachments/<sha256>.<ext>
    projects/<id>/covers/<key>-<name>.jpg   derived cover pictures (front, back, thumb; the key is made from the layout hash, the rules' version and the book's direction, COVERS.md); made on first use inside the compile gate, swept when the layout changes, not backed up
    .tmp/                   uploads being received (swept at start)
    imports/<id>/
  mirror/
    <project-slug>--<id suffix>/   unambiguous: slugs are unique per owner only
      project.json         manifest (version 2): project fields, state, every document with its outline data and hash
      README.md            "generated; edits here are not read back"
      compendium/          present when the project uses a Compendium: a read-only projection (Phase 3)
        README.md, index.json
        entities/<kind>/<name slug>--<id suffix>.md   with the book's overrides applied
        timeline.md          placeholder until the timeline is written
        research/<compendium>--<id suffix>/   the research of each Compendium the project uses
      manuscript/
        00-front-matter.md
        01-part-one/
          01-chapter-one.md
          02-chapter-two.md
      notes/
      research/            the project's own research notes, folders as directories
      attachments/         the files the manuscript links to, byte for byte
      characters/          (Phase 3)
    compendium-<slug>--<id suffix>/   a Compendium no project uses as its primary
      compendium.json, README.md, timeline.md, entities/<kind>/…
    .state/<project or Compendium id>.json   what was last written; outside every folder
    .tmp/                      temporary files, same file system, swept at start
  backups/
    2026-11-12T16-30-00.zip         manual (also auto-<time>.zip, pre-restore-<time>.zip)
    migrate/                        pre-migration copies
  conflicts/
**Database is canonical. The mirror is a projection.** It is written asynchronously after each mutation, debounced per project (2 s, never later than 15 s after the first unwritten change), and atomic per file (write temp, rename; unchanged files are not touched). `internal/mirror` follows the events the ops write, so a new op is covered by emitting one. Failures are retried with backoff and shown by `mirror.status`; `mirror.rebuild` repairs a project against the bytes on disk. The folder name, the state record, the `project.json` schema (version 2: each document's synopses, point of view, location, story dates, word target and compile flag, which the importer reads back) and the Git push's use of `Mirror.Hold` are in `docs/OPEN-QUESTIONS.md` §40. It exists so that `grep`, `git`, `rsync`, and a text editor always see the work, and so that a catastrophic bug is survivable. It is explicitly not a second write path in v1: a watcher that imports hand-edits is a Phase 7 experiment, because bidirectional filesystem sync is where note apps go to die.

Each project mirror carries a read-only `compendium/` projection of the entities, research, and timeline it references, so one repository clone is readable on its own. A Compendium also has its own export for the full bible. Neither projection is read back automatically; import is explicit.

Attachments are content-addressed by SHA-256, deduplicated per owner (a project or a Compendium), and served only through an authorized route with `Content-Disposition` and a sniffing guard. The mirror holds the ones a document links to under `attachments/`, listed in `project.json`; research notes are written under `research/` (and a project's view of its Compendiums' research under `compendium/research/`), one Markdown file per note with front matter (id, kind, title, status, tags, source, retrieved, quote). A person's scratchpad is in neither.

### Uploads and files

An upload is the one op whose body is a file. It is registered with `Op.RawBody`: the REST binding authorizes the caller (and refuses a preview, since an upload writes a file) *before* a byte of the body is read, then hands the handler the body as a stream. The handler hashes it into `data/files/.tmp` under a hard size limit, decides what it is from its bytes (magic bytes and `http.DetectContentType`; a name or declared type that contradicts them is refused), and then, holding one short lock and one short transaction, de-duplicates, checks the quotas against the counters the database keeps (triggers, so a purge's cascade is counted), moves the file into place and writes the row. A file with no row is an orphan a sweep can remove; a row with no file would be a broken image, so the file goes first and is removed again if the transaction fails. A delete removes the row, then the file, unless a concurrent upload of the same bytes has made it live again (the same lock). The path of a stored file is always rebuilt from the owner id, the hash and an extension from a short list; nothing a client sent is ever part of it.

`GET /files/{id}` serves the bytes to a signed-in person who can read the owner (a project's or a Compendium's membership, through the registry's own checks). A missing file and one the caller cannot read are the same 404. The type is the stored one; only images and PDFs are shown inline; the response carries `X-Content-Type-Options: nosniff`, a `Content-Security-Policy` of `default-src 'none'` (plus `sandbox` for everything but a PDF, whose viewer a sandbox would stop), an ETag and `Cache-Control: private, no-cache`, and ranges work. API keys read files through the `*.attachment.download` ops. Markdown refers to an attachment as `![alt](/files/att_...)`; saving a document or a note records the links in `attachment_refs`, which is what the delete check and `*.attachment.refs` read, and exports keep the Markdown exactly as stored.

### Live updates

`GET /api/v1/events?project=<id>` or `?compendium=<id>` is a `text/event-stream` of what happens to one project or one Compendium. It is authorized exactly like `project.get` or `compendium.get` (404 for no access, a key's pin and scope honoured, no CSRF for a GET) and again on every heartbeat, so a removed member, a revoked key or an ended session loses the stream within half a minute (`event: bye`).

- **Frames.** `id: <seq>`, `event: <type>`, `data: {"seq","type","project_id"|"compendium_id","document_id","by_you","at","payload"}`. `seq` is the event's number in the `events` table: commit order, one sequence for the instance. `by_you` says whether the caller made the change; no other person's id is sent.
- **First frame.** `retry: 3000`, then `event: hello` with the current `seq`, so a quiet stream still has a cursor to resume from (`resumed: true` after a replay).
- **Resume.** A reconnect sends `Last-Event-ID` (or `?last_event_id=`). Events after it for the same scope are replayed in order, then the stream goes live with nothing missed or repeated (the hub subscribes before it replays, and the live feed starts after the replay's last number). If the client is further behind than the window (1,000 events or 24 hours, configurable) or its number is from the future (a restored database), it gets `event: reset` and must reload; the stream then continues live from the new cursor.
- **Heartbeat.** A comment (`: heartbeat`) every 25 seconds. A write that does not complete in 10 seconds drops the client.
- **Backpressure.** Each stream has a buffer of 256 events. A client that falls further behind is sent `event: reset` (reason `slow_client`) and closed; it reconnects with its last id.
- **Limits.** Per person and per instance (`SCRIOBH_SSE_PER_USER`, `SCRIOBH_SSE_TOTAL`); over them a 429. Every stream ends with `event: bye` when the server shuts down.
- **What an event may carry.** Ids, types, revisions and counts, and nothing else: no title, name, text, address or file name. A payload key must be on the allow-list in `internal/events/payload.go` and a value must be a number, a flag or a short token, or it is dropped before it leaves the process; a test drives every op and checks the events table against the same list. A project stream also carries no Compendium or entity id, and a Compendium stream no project id, so being on one is never a way to learn the other's ids. An event that names both a project and a Compendium is delivered to both, each stripped of the other's ids.
- **Presence (F-244).** A project stream also carries `event: presence` (no `id:` line, so it never moves the resume cursor) for a person who has `presence.share` on and a browser session (a key's stream gets none): the list of who else has the project open, built for that person in the stream's own goroutine, at most one frame in 5 seconds, sent when it changes. It does not pass through the hub or the events table, so it is never replayed and cannot reach a recipient the per-viewer filter excludes. The table behind it is memory only (`internal/domain/presence`); see [p7-notes/presence.md](p7-notes/presence.md).
- **Save now (desktop mode only; docs/UPDATES.md, contract change 31).** A writing page's stream (its live marker has `data-flush`, so `js/live.js` adds `flush=1`) registers with the desktop shell's "save now" round and is sent `event: desktop.save` with `{"n","flusher"}` (no `id:` line, so it never moves the resume cursor and never passes through the hub or the events table) when the shell is about to restart the app for an update; `js/doc.js` answers by running its ordinary `flushAll()` and posting the result to `POST /desktop/saved`. A server outside desktop mode ignores `flush=1` and has none of the routes.
- **Fan-out.** One goroutine reads the new rows when the events store signals a commit and hands each to the subscribers of its project and of its Compendium; it exists only while someone is subscribed. Nothing polls per client. The scratchpad writes no event.

---

## Alternatives, ghosting, and annotations

Alternatives are annotations on text, not a formatting trick. The design, the invariant and the algorithms are in [OPEN-QUESTIONS](OPEN-QUESTIONS.md#phase-35-alternatives-decisions); this is the shape of it.

- **The chosen text is the document.** `documents.content` holds whichever wording is selected, followed by one trailing comment. Search, word count, export and the mirror all read ordinary Markdown. The other wordings live in `alternatives` and `alternative_variants`, which are canonical; the comment is their serialisation, and the server rewrites it in the same transaction as any write that could disturb it (`internal/domain/altstore`, called from `doc.update`, `revision.restore`, `doc.split`, `doc.create` and the importer). A save from a client that sends no comment keeps the alternatives; a comment that differs from the tables is applied as an edit.
- **Anchoring.** An anchor is a range of the body with evidence to find it again (offsets in UTF-16 units, 24 characters of context each side). On a save, the one edit between the old body and the new one decides: anchors before it stay, anchors after it shift, exactly. An anchor the edit touches is searched for (full context, then shorter context in the rewritten region, then a long passage that occurs once); if the edit lies inside it the wording was edited in place and the new text is adopted (the original is immutable, so it becomes a new wording); otherwise the alternative is `orphaned`, listed for review and re-attached only by an exact match or `alternative.reanchor`. It is never moved by a guess and never dropped. Ops that know their edits pass them, and nothing is searched.
- **Cycling is an edit, not a preview.** Choosing a wording writes it into the body, records a revision (`reason='alternative'`), and emits a normal document event. Undo is choosing the previous wording or the original, which is immutable. It earns no goal credit.
- **Rendering.** The editor's decoration layer draws the wavy underline, end dots, and the middle-dot indicator. The rich surface is the only input; the Source view is a plain textarea over the same Markdown. What it must do is specified in [EDITOR-SPIKE](EDITOR-SPIKE.md).
- **The Lab returns annotations.** A scan produces ranges with a kind and a reason. Accepting one applies a text edit in a transaction and writes a revision; rejecting one only changes the annotation's status. Nothing is rewritten without that step. (Phase 4.)
- **Ghosting lives in the Markdown.** A ghosted range is wrapped in `<scriobh-ghost>` and `</scriobh-ghost>`; the text is never removed. The word count, the search text and mention matching leave it out, and export includes it unless the export asks otherwise. The `ghosts` table is a derived index.
- **Stash is separate.** It is not part of `documents`, not in the manuscript FTS index, not exported and not counted. It has its own index and is mirrored to `stash/`, which Git never carries. Putting an item back is `stash.drag_back`, an edit of the document.
- **Limits.** At most 200 alternatives per document (`SCRIOBH_MAX_ALTS_PER_DOC`, 1 to 5000) and 50 wordings per alternative.

### Markdown representation

The stored text ends with one comment line when alternatives exist:

```markdown
<!-- scriobh:alts {"version":1,"sig":"…","items":[{"id":"alt_…","scope":"word","before":"The ","after":" house was empty, and th","chosen":"alv_…","variants":[{"id":"alv_…","text":"quiet","original":true,"at":"2026-11-13T10:00:00.000Z"},{"id":"alv_…","text":"still","at":"…"}]}]} -->
```

It is valid Markdown, invisible in every renderer, and round-trips through export and import. It carries no offsets, so typing away from an alternative leaves the line byte-identical (`sig` fingerprints what it says, so an older copy of the comment can be told from an edit); the importer finds each item by its text and context. The reader also accepts a short hand-written form (`{"scope":"word","quote":"quiet","variants":["still","low"],"chosen":"quiet"}`). Inline `[a|b]` syntax is explicitly rejected: it is unreadable outside this app and fragile in prose. There is no sidecar file: a set too large for one comment is refused.

---


## Compendiums and shared material

A **Compendium** is a first-class library item: entities, research, clippings, timeline, glossary, and rules. It can exist **with or without a project**, so a writer can build one for future books or keep one purely as a reference. A project has **at most one primary Compendium** and may reference entities or whole Compendiums from others.

- **Scope resolution is explicit.** When a project looks up an entity, it resolves in this order: project-local entity → primary Compendium entity → explicitly referenced entity from another Compendium. A project-scoped override (`project_entity_overrides`) adjusts fields for that book without touching the Compendium record.
- **Access is not inherited.** Compendium membership is separate from project membership. A beta reader invited to one project does not get the bible. A project editor who needs the shared Compendium is added to it explicitly. This is the only safe rule when one Compendium supports books with different collaborators.
- **References, not copies.** A project can reference an entity from another Compendium in `reference` mode: it appears in the scene sidebar and mentions, but cannot be edited from that project. `copy` mode creates a local entity with a provenance pointer. Whole-Compendium references work the same way for research and timeline material.
- **One timeline, several orders.** Compendium events carry a story date; each project records its own narrative order. A series bible stays consistent while each book orders events differently.
- **Collaborator add is a prompt.** Adding a project editor prompts to add them to the primary Compendium (default yes); reviewers and readers default to no. Compendium access is never granted silently.
- **Deleting is scoped.** Deleting a project never deletes Compendium material. Deleting a Compendium requires an explicit confirmation that names every project that will lose it.
- **Move and merge are reviewed.** Changing a project's primary Compendium, merging two Compendiums, or splitting one is a previewed operation with a dry-run count of entities, overrides, and references.

Queries always carry the Compendium scope and apply the project override in one place (`internal/domain/entities`), so a page cannot accidentally show the wrong version of a character.

### Mentions and the timeline

Package layout is dictated by one constraint: the document write path (`docs`) updates the mention index, and `entities` imports `projects`, which imports `docs`. So the leaf packages come first and the rest builds on them.

```
text        the word matcher and the Markdown span finder (no database)
storydate   partial story dates, their sort keys and intervals (no database)
vocab       a project's reachable entities and names, light, plus the reader filter
mentions    the index, the stamp, the background worker, the mention ops
scenes      point of view and location as entities, the sidebar, the free-text repair
docs        calls mentions.Sync in the same transaction as the text
entities    imports projects; the timeline registers its mirror and fingerprint hooks here
timeline    events, per-book order, the project view, the contradiction finder
```

**One index, three writers, one seam.** `entity_mentions` is written by the document ops (the document's own rows, inside the save), by the worker (a whole project against a changed vocabulary) and by the entity merge step. Anything that changes a project's vocabulary changes `mention_state.stamp` through triggers on the entity, alias, override, reference, link, primary-Compendium and Compendium-state tables (migration 1520), so a new op or a bulk loader cannot forget to. A save reads the stamp and, if the cached matcher is for another stamp, the vocabulary, in the writer transaction it is already in; the cache is keyed by project and stamp, so an entry is never wrong, only unused. The worker reads the plain text (`doc_text`, which the save already wrote, so no Markdown is parsed), matches outside the writer, and writes only differing rows, re-checking each document's content hash and the stamp inside every chunk.

**Privacy by filter at the edge.** The index holds entity ids for every entity in the project's scope. What a caller sees is filtered at read time through `vocab.LoadVisible`: Compendium membership decides, project access never does, an unreadable entity is the same not found as a missing one, and an ambiguity is only reported when the other meaning is readable.

**Two orders on one timeline.** Story order is the sort key of the dates (storydate); narrative order is the reading order of the scenes that tell things, with each book's optional own order of events permuting events among the places events hold. A book's view, its contradiction check and the `timeline.md` file are all built from the same loaded data (`pdata`), so they cannot disagree.

---

## Authentication and authorization

**OIDC only.**

- Authorization code flow with PKCE S256, `state` and `nonce` in short-lived signed cookies.
- Claims: `sub`, `email`, `email_verified` (must be true or `"true"`), `name`, `groups`.
- Identity is `issuer|sub`, stored once. Linking by email only to an unbound account, guarded by a conditional update; an email match on an account bound to another subject is a hard failure with an audit line.
- Sessions: `HttpOnly`, `SameSite=Lax`, `Secure` when the base URL is https, 24 h absolute, configurable idle cap, revocable, listed per user.
- Roles come from groups, not from a local role editor:

| Group | Role |
| --- | --- |
| `scriobh-admins` | instance admin: users, config, storage, AI policy, all projects |
| `scriobh-writers` | can create projects and write |
| `scriobh-readers` | can read projects they are invited to, nothing else |
| `scriobh-team-<name>` | optional grouping for future sharing |

Without an admin group configured, the first successful login may claim admin once, behind a confirmation screen that names the identity. After that the claim is closed permanently.

### Development identity provider

Phase 0 does not wait for a production IdP. `cmd/scriobh-dev-idp` runs a minimal OIDC issuer on loopback with discovery, authorization, token, and JWKS endpoints, and three fixed test users:

| User | Group |
| --- | --- |
| `admin@dev.scriobh` | `scriobh-admins` |
| `writer@dev.scriobh` | `scriobh-writers` |
| `reader@dev.scriobh` | `scriobh-readers` |

It is a real issuer: the client uses the same discovery, PKCE, state, and nonce path as production. It is not a bypass.

- Enabled by `SCRIOBH_OIDC_STUB=1`.
- Refused at startup unless **both** `SCRIOBH_BASE_URL` and the listen address are loopback: the issuer has no credentials, so anyone who can reach the port could sign in as an admin. The dev compose stack alone opts out of the listen check with `SCRIOBH_DEV_ALLOW_ANY_LISTEN=1` and publishes its port to `127.0.0.1` only.
- Development users are stored under the fixed issuer `urn:scriobh:dev-idp`, so identity survives a change of ephemeral port.
- Excluded from production builds with the `prod` build tag (`make build`, the image).
- One issuer package (`internal/oidcstub`) serves two shapes: mounted in-process at `/dev-idp/` on the app's own origin (so the issuer URL is identical from the browser and from the app, and `docker compose up` works), and as the standalone `cmd/scriobh-dev-idp` binary.
- Discovery is lazy: the client contacts the issuer on the first login, never at startup, so an app can use its own mounted issuer. With an ephemeral port the base URL is derived from the bound listener.
- The test suite starts the same stub for end-to-end auth tests.

### Production identity with Passage

The maintainer's IdP is [Passage](https://github.com/crueber/passage), an OAuth 2.0 / OIDC provider. Register Scríobh as an application in Passage:

- Callback: `${SCRIOBH_BASE_URL}/auth/oidc/callback`
- Scopes: `openid profile email`
- App-scoped groups: `scriobh-admins`, `scriobh-writers`, `scriobh-readers`
- `client_id` and `client_secret` from Passage, stored in env

Passage emits app-scoped `groups` in the ID token; Scríobh maps them on every login. No local credentials are involved.

**Authorization** is per project, checked in one package:

- `owner` — settings, members, delete, export, all content.
- `editor` — read, write, comment, AI, export.
- `reviewer` — read, comment, suggest; no direct edits. Reads every comment and suggestion of the project; resolves only threads they started; withdraws only their own suggestions.
- `reader` — read only. Cannot see comments.

Missing access returns `404`, not `403`, so project existence does not leak. A sweep test asserts this for every op. A table test (`roles_test.go`) pins the role of every project op: a GET needs the reader role and anything else the editor, except the listed exceptions with their reasons.

### Review and sharing

**Comments** (`internal/domain/review`, anchors in `internal/domain/comanchor`) are rows beside the text, anchored to a range of the body by the alternatives machinery: the same `alts.Rebase`, run in the **same transaction as every write to the text** (`doc.update`, `revision.restore`, `doc.split`, and the alternatives, ghost, drag-back and accept seams: the four places that update `documents.content`), one indexed query when the document has no comments. An edit before, after or in the middle of the commented words moves, keeps or follows the anchor; words that are gone orphan it. `TestAnchorInvariantAfterEveryWritePath` drives every write path at random and checks that an anchored comment always quotes exactly the text it points at. Comments never enter an export, the mirror (their events are ignored by it), a Git push, the search text or an AI context. Mentions are members only, in-app only: the notification rows hold ids, there is no email and no push, and the screens poll `notification.count`.

**Suggestions** are rows too. Accepting one is an edit of the document through the same seam as an accepted AI suggestion (`docs.ApplyReviewEdits`: the revision checked, hidden ranges and alternatives refused, replacement text held to the one safety filter, one revision with reason `suggestion-accepted`, the text before kept, no goal credit). It is created with the same checks (`docs.CheckReviewEdits` runs them without writing), so a change that could never be applied is refused when it is proposed.

**Share links** give a person with no account read-only or comment-only access. They are not registry ops: a registry op's caller is a person. The public routes (`/s/{token}/…`, `internal/server/share.go`) are one table (`shareEndpoints`) behind one entry point, and the sweeps enumerate the table. Principal: a `share.Link`, not an `ops.Principal`; it carries what the link allows and no person. Token: `<id>.<256-bit secret>`, only the SHA-256 kept, compared in constant time; every token that opens nothing (garbage, wrong secret, unknown id, revoked, expired, project in the trash) and every unknown path under `/s/` gets one 404 with one body. The session and API-key middleware do not run for `/s/`: nothing is set, no cookie is read. `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex`, no caching, the same strict CSP, a 32 KB body limit, and the token never in a log line (the log records the route pattern; the two places that logged a path redact `/s/`). Rate limits: per client address first (before the token is looked at), per link after, tighter for unlock attempts and guest comments; wrong passphrases are counted per link and per client address (an IPv4 address, an IPv6 /64) with an escalating wait, so a stranger locks only themselves out, and a passphrase link's own allowance is spent only by callers holding the proof. A passphrase is argon2id-hashed (two hashes at a time for strangers, two of the owner's own), proved by an HMAC proof the client sends in `X-Share-Unlock` (no cookie). A link serves rendered reading text only, by the whitelist renderer (and never a document left out of the compile): no private notes, comments, front matter or alternatives annotation, ghosts left out unless the owner chose otherwise, images as notes, and never notes, research, the stash, scratchpads, settings, members, history, other links, exports (its own download flag aside) or anything AI. Guests see only the threads started through their own link.

**Machines** use API keys (`Authorization: Bearer` or `X-API-Key`), scoped and expiring. Keys are hashed at rest (SHA-256 of a random 32-byte secret, prefix stored for display). Agents get their own principal with an `is_agent` flag so the UI can badge them and the audit log can separate them from humans.

---

## The AI subsystem

In one sentence: a provider interface behind a connection guard, a context builder that shows its work down to the bytes, a policy the server enforces, and a run record for every call. Full design in [AI.md](AI.md); the decisions and the seam for recipes in [OPEN-QUESTIONS](OPEN-QUESTIONS.md#phase-4-ai-core-decisions).

```
ai.context.preview / ai.run / POST …/ai/run/stream
  → kill switch, request shape, per-person rate        (nothing read yet)
  → build: policy (mode, action, agents) → context chips (read inside the project, never-send,
           Compendium read rule) → the recipe's prompt → redaction → framing with a per-request
           boundary → the exact body for each provider in the chain → payload hash → estimates
  → run only: the preview's hash must match; concurrency slot; budget check + reservation (one tx)
  → providers in order, each behind the connection guard, a retry for what retrying can fix
  → the answer restored, parsed by the recipe, returned as a suggestion with a run id
  → settlement: usage, cost, audit entry and event in one transaction
  → result hook (internal/domain/aiwork): anchors the answer against the document the model read and
    keeps what must outlive the response (a suggestion for 30 minutes, a review, the Lab's marks)
```

**Results and accepts.** The hook lives inside `Service.Run`, so a streamed run and a plain run are alike. Nothing reaches a document except an accept op (`ai.accept.edit`, `ai.accept.alternative`, `ai.lab.accept`, `ai.lab.accept_all`, `ai.summary.accept`, `ai.extract.accept`), which applies a text the server holds, verified against the run's recorded output hash, through the document's own write seam (`docs.ApplyAIEdits` and friends, built on the alternatives session) in one transaction, with the history reason `ai-accepted` and no goal credit. The recipes' parsers (`internal/ai/recipes_*.go`) never read a position, bound every list and length, and check what a model cites against the chips that were sent; the Lab's local analysers (`internal/ai/analyse`) need no policy and no provider. See [OPEN-QUESTIONS, "Phase 4 recipes decisions"](OPEN-QUESTIONS.md#phase-4-recipes-decisions).

A recipe (`internal/ai.Recipe`) is code that turns validated parameters and loaded chips into a prompt, and optionally parses the answer; it has no access to the database, a provider or the policy. Adapters (`internal/ai/client.go`, `wire.go`) turn a provider-neutral payload into each service's body with structs, never maps, so the bytes are a function of the inputs; no body has a `tools`, `functions` or `tool_choice` field. Adding a provider kind means one case in `Encode`, one in the response parsers and one in the model list parser; adding an action means a recipe. Nothing in this path writes to the manuscript.

---

## Export pipeline

| Format | Implementation | Notes |
| --- | --- | --- |
| Markdown | native | single file or bundle |
| HTML | native: `text.RenderHTML` over the parse tree the counter reads | self-contained, print CSS included, raw HTML shown as text, strict page CSP (`export.html`) |
| DOCX | native OOXML writer (`internal/publish`, zip + XML by hand) | `export.compile` with a preset: US Standard Manuscript (Shunn), UK manuscript, styled; real Word styles, a PAGE-field header, real footnotes, embedded attachments, no external relationship, no macro |
| Plain Markdown (compiled) | native, from the same structure as the DOCX and EPUB | `export.compile` with the `plain-markdown` preset: the chosen text only |
| Submission bundle | zip of a manuscript DOCX, a synopsis, a cover letter template and a manifest | `export.submission` |

### Fiction compile presets

Two manuscript presets ship first, because they are what agents and editors ask for:

- **US Standard Manuscript (Shunn):** Times New Roman 12 pt, double-spaced, 1-inch margins, first-line indent 0.5 inch, no extra paragraph spacing, title page with contact details and word count, header `Surname / TITLE / page`, each chapter on a new page, scene breaks as a centered `#`.
- **UK manuscript:** A4, Times New Roman 12 pt, double-spaced, a title page with a "Word count" line, header `Surname / Title / page`, same chapter and scene-break rules.

Both are presets over the same option set: font (Courier New or Times New Roman), size, line spacing, margins, header format, scene-break style, chapter page breaks, and first-line indent. The DOCX uses real Word styles (`Title`, `Heading 1`, `Normal`) and a header field (`PAGE`; the title in the header is text, not a `STYLEREF`) rather than direct formatting, so the writer can still edit the file in Word without fighting the export. A compile preview renders the first pages before the file is produced.

"Styled" and "ebook" presets remain separate; the manuscript presets are deliberately plain.

**The compile pipeline** (`internal/domain/compile`, ops `compile.preview`, `export.compile`, `export.submission`, `compile.preset.*`, `compile.contact.*`) is selection first and writing second. Selection (documents marked "do not compile", the notes, and everything inside them are out; front matter first, back matter last; headings, scene breaks and numbering by the preset's options) is one function shared by the preview and the export, so the warnings and the input hash are the same in both. Writing is `internal/publish`, a pure package: a `Book` (sections of the neutral structure `text.Structure` builds from the parse tree the counter reads) and `Options` in, bytes out, no database, no clock, no disk. The built-in presets are code; a custom preset is a row of `compile_presets` that stores its options in full.

**The print pipeline** (`publish.LayoutPDF`, then `Render`) is the same Book and Options with a typesetter in between. Paragraphs are shaped (NFC, one glyph per character, legacy `kern` pairs, fallback faces, a replacement and a warning for anything no face has) into boxes, glue and penalties and broken by Knuth-Plass with a first-fit fallback for paragraphs of more than about 25,000 words; lines become boxes in a vertical flow with keep rules (orphans, widows, headings with what follows, scene breaks, table headers) that is poured into pages with footnotes at the foot; front matter, contents and body are numbered from the layout. **All lengths are integers in thousandths of a point**, so the same input gives the same lines, pages and bytes on any processor. The preview and the export run the same `LayoutPDF`; the preview does not render. A compile\'s `input_hash` also names the engine (`publish.EngineVersion`: typesetter, fonts, PDF, DOCX and EPUB writers), so the same hash is the same bytes across upgrades. Compiles run through a gate (`SCRIOBH_COMPILE_CONCURRENCY`) and a PDF has a word cap (`SCRIOBH_COMPILE_PDF_MAX_WORDS`). The file is written by `pdfw` (objects, a cross-reference table, Flate streams, Type 0 fonts with ToUnicode, a page tree, page labels, an outline) with no clock and no dependency.
| EPUB 3 | native zip + XHTML + OPF + NCX (`internal/publish`) | `export.compile` with the `epub3` preset. Checked by a Go structural validator in the test suite (`internal/publish/check`); epubcheck is not run, see [OPEN-QUESTIONS, "Phase 5 export and import decisions"](OPEN-QUESTIONS.md#phase-5-export-and-import-decisions) |
| Static site | native: `internal/site` (pure, over `text.Structure`), `internal/domain/site` (selection, options, ops) | `site.build` (a zip) and `site.publish` (a folder). HTML that is also well-formed XML, one stylesheet, no script or inline style, RSS, Atom and a sitemap given a base address; checked by a Go validator in the test suite (`internal/site/check`), not by a feed reader or a search engine |
| PDF (print) | paged HTML → headless Chromium | optional binary; graceful error if absent |
| PDF (fallback) | browser print stylesheet | works everywhere, no dependency |

| PDF (print) | native: `internal/publish` typesetter, `internal/publish/pdfw` writer, `internal/publish/font` (embedded Liberation and DejaVu TrueType faces) | `export.compile` with a print or proof preset; PDF 1.7, subset fonts with ToUnicode, deterministic; validated by `internal/publish/check`. Replaces the earlier plan of paged HTML through headless Chromium: one binary, no optional dependency (invariants 2 and 10), see [OPEN-QUESTIONS, "Phase 6 print decisions"](OPEN-QUESTIONS.md#phase-6-print-decisions) |
| Paged HTML | native, from the same structure | `export.compile` with `paged-html`: CSS paged media in one self-contained file to print from a browser |
| Print cover | native PDF | `export.cover`, with `compile.cover_spec` for the spine and wrap |

**The static site** (`internal/site`, ops `site.*`) follows the compile pipeline's rules and adds three of its own. *Selection first*: `internal/domain/site` decides which documents are posts or pages from the tree, the compile flag and the status (only `done`, or `draft` and `revise` with `include_drafts`), the same subtree rule a compile uses; the generator is handed entries and never reads the database. *Hidden text never reaches the renderer*: the alternatives annotation and ghosts are removed first, and `text.Structure` drops private notes, HTML comments and front matter; the generator renders from that neutral structure, not from `text.RenderHTML`. *No clock in the output*: dates are the publication dates, a feed's build date is the newest post's, GUIDs are hashes of ids, so the same input is the same bytes. `site.publish` is the only op that writes outside the database and the only place a folder is touched (`internal/site/dir.go`): the folder comes from `SCRIOBH_SITE_DIR`, never a request, and the write is a sibling folder renamed into place that replaces only what a record says it wrote. Pushing the site to a Git branch is not built (the Git engine is a single branch of the manuscript).

Exports are deterministic: same input, same bytes (except timestamps, which are fixed from the project's `updated_at` where possible). Each export records a manifest of included documents and their hashes, so a later "what did I send my editor?" question has an answer.

---

## Git push-only sync

A project can push its Markdown mirror to a repository the writer owns. **Push-only is the contract**: Scríobh writes, it does not read. Anything coming back is an explicit import, reviewed like any other import.

This is **not multi-device sync**. The server is the single writer; the repository is a versioned copy for backup, publishing, reading elsewhere, and CI. Two Scríobh instances do not share a database through Git.

### What is pushed

```
<branch>/
  scriobh.json                 the mirror's project.json, published under this name (see OPEN-QUESTIONS, Phase 2 Git sync decisions)
  manuscript/
    00-front-matter.md
    01-part-one/
      01-chapter-one.md
  notes/                       if the project includes them
  characters/                  not pushed: the bible stays in the mirror (see OPERATIONS, "The bible and Git sync")
  attachments/                 referenced images only
```

Never pushed: `scriobh.db`, sessions, API keys, AI credentials, AI transcripts, audit logs, comments or revisions (they are metadata, not manuscript), or any file outside the mirror.

### Commits

- One commit per push, not per save. Pushes are coalesced with a 30 s debounce per project, plus manual "Push now" and an optional interval.
- A stable author identity set on the remote by the owner, defaulting to the neutral `Scríobh <noreply@scriobh>` (the default names no one: a repository can be public). No hostname, no timestamp in the message body, so a rebase does not churn history. If the project sets an author name and email, those win.
- Message: `The Salt Road · ch 12: +214 words` and a body listing changed chapters. Deterministic order.
- A pre-push check refuses the push if the tree contains something that looks like a secret (private key header, `sk-…`, bearer token) and names the file.

### Auth and host verification

- **SSH deploy key is the primary path**, with presets for Forgejo and GitHub that prefill the URL shape and say where the key goes. A preset carries no host key: the host key is scanned and accepted explicitly. The form stays generic for any SSH remote.
- **HTTPS token** is the fallback, encrypted and write-only, for hosts that do not allow deploy keys.
- **SSH deploy key**: ed25519 generated in-app; the public key is shown for copy (it is not secret); the private key is encrypted with `SCRIOBH_SECRET_KEY` and never leaves the server. The host key is scanned, its fingerprint shown, and pinned on an explicit accept, never silently on first use. A changed key is a hard error. No `StrictHostKeyChecking=no`.
- Both are per remote, revocable, and exercised by a "Test connection" button that performs a read-only `ls-remote`.

### Divergence policy

Before pushing, Scríobh runs a read-only `ls-remote` and compares commit ids against its own local history. No objects are fetched to decide, no working tree is touched. (A refused push fetches the remote branch into a scratch folder, only to list the commits that are not ours; see OPEN-QUESTIONS.)

| Remote state | Behaviour |
| --- | --- |
| Equal, or an ancestor of local | Push normally |
| Ahead (someone else pushed) | The push is refused and the remote commits are shown. The UI offers **Force push** — typed confirmation, a local bundle of the remote ref written to `data/backups/git/`, then the push — or **Import** the remote into a new project. There is no fetch-and-merge |
| Unreachable | Keep the local commit, show the error, retry on the next trigger or manually |

There is no auto-pull, no rebase, no merge, and no "keep both" file naming. The only path from remote to local is the import flow below. Force-push overwrites the remote; it is the writer's explicit choice, not a fallback the app takes on its own.

### Import from a repository

1. Enter an https address (and a token for a private repository), or pick the remote of one of your projects. A local directory is not accepted: it would let any editor read the server's files.
2. Clone (shallow) into `data/imports/<id>/`; never into the mirror.
3. Recognize exactly two layouts (the folder and zip import, which a person drives from a preview, also takes a Word document, a Scrivener project, a WordPress export, a Ghost export and, when the person says so, an Obsidian vault: see "Other sources" below; a Git import never does). **Scríobh mirror**: `scriobh.json` (or the mirror's `project.json`) names the project and the numbered `manuscript/` tree gives the structure; see [OPEN-QUESTIONS § 39](OPEN-QUESTIONS.md#39-phase-2-goals-and-import-decisions--decided). **Generic Markdown folder**: folders become parts, `NN-title.md` becomes chapters, headings become scenes. No Hugo, Jekyll, Obsidian, or other layout detection.
4. Show the proposed tree, the word counts, the front/back matter, and any files that will be skipped. The user edits the mapping before anything is written.
5. Create the project in one transaction, store the source URL, commit hash, and file hashes as `project_sources`, and keep the clone as the provenance copy.
6. Re-import later is a separate, reviewed action against the same source; it produces a diff, not a merge.

### Other sources: Word documents, Obsidian vaults, Scrivener projects, WordPress and Ghost exports

`import.preview`, `import.apply` and their `append` forms also take `docx` (a .docx, base64) or `layout: obsidian` (never detected). Both are converted, before the importer's own intake, into the folder of Markdown files the generic importer already reads (`internal/imports/docxconv.go`, `obsidian.go`), so the preview, the edits, the token, the caps and the provenance are the folder import's. What each conversion changed or dropped is in the preview's warnings; the provenance is `project_sources` kind `docx` (with the file's own hash) or `markdown` (with the layout in the metadata).

`layout: scrivener | wordpress | ghost` (files or a zip) reads the three sources of Phase 6. Each source has a converter that reads the foreign project into a tree of documents and a list of research notes (`convert.go`: `cdoc`, `rnote`, `cresult`), and `toFiles` lays the tree out as the **Scríobh layout** the importer already reads (`manuscript/NN-title/00-title.md`, numbered by order) with a `scriobh.json` the converter writes itself (titles, kinds, statuses, synopses, the compile flag, a story date, a source path). The manifest is never read from the source, so a foreign file cannot name a path, a kind or an entity. Every converter writes Markdown through **one emitter** (`mdsink.go`, over the DOCX converter's escaping): paragraphs are built from segments, every character that could be read as Markdown or as a Scríobh construct is escaped, `%%private notes%%` are paired per text block, and footnotes are renumbered. The readers are `rtf.go` (RTF, one pass, constant work per byte), `xdom.go` (XML under a token budget, a depth limit, no entities and no DOCTYPE; UTF-16 and single-byte encodings decoded first) and `htmlmd.go` (a tokenizer from `golang.org/x/net/html` and a whitelist of elements; never a tree, never raw HTML out); Ghost's Markdown cards go through goldmark and the same walker, after `mdbound.go` has broken up the shapes that make a Markdown parser quadratic. Research notes (Scrivener's Research folder) are written by `research.InsertImported` in the import's own transaction; they are part of the token. The layouts are recognised without being chosen only by their own markers (a `.scrivx` file; a lone `.xml` that says it is a WordPress export; a lone `.json` that says it is a Ghost export) and only when the files hold no Markdown. The provenance is `project_sources` kind `markdown` with `layout` and a report of counts in the metadata (`kind` is a closed list in the table; see DATA-MODEL).

### Implementation notes

- `go-git` for clone and push keeps the binary self-contained. `SCRIOBH_GIT_BINARY` (a system git for a remote that needs a feature go-git lacks) is **not built**; an error says what failed and names the host.
- One push per project at a time, guarded by an in-process lock and an `IMMEDIATE` transaction on the remote row.
- Push work runs in a bounded background worker, not in the request. The UI shows state, not a spinner.
- The mirror is already atomic; the push reads a snapshot of the mirror tree, so a save during a push cannot produce a half-written commit.

---

## Desktop-ready constraints

Every phase must keep the app wrappable. These are tested from Phase 1, not discovered in Phase 8.

| Constraint | Requirement |
| --- | --- |
| Bind address | `127.0.0.1` with a configurable or ephemeral port. Never assume 8080 |
| URLs | Derived from `SCRIOBH_BASE_URL` or the request host. No hardcoded absolute URLs in templates or redirects |
| Cookies | `HttpOnly`, `SameSite=Lax`, `Secure` only when the base URL is https, so `http://127.0.0.1:<port>` works |
| CSP | `self` only; no inline scripts. The wrapper injects nothing into the page |
| Reverse proxy | Not assumed. `SCRIOBH_TRUST_PROXY=false` by default |
| Service worker | Optional. The app works without one; it keeps only `/static` and an offline page, never a page |
| Filesystem | Only through the server API. The web layer never gets a filesystem bridge |
| PDF | Server-side export; `window.print()` is a fallback, never the only path |
| Identity | The embedded issuer is a real OIDC provider (discovery, authorization, token, PKCE), not a bypass. The same middleware and session code run |
| Secrets | OS keychain when a wrapper provides one; otherwise the encrypted file |
| Lifecycle | `/healthz` for the wrapper to wait on, single-instance lock, graceful shutdown |
| Tests | A smoke test starts the binary on `127.0.0.1:0` and drives the app at the printed port, with no reverse proxy and no external IdP |

The wrapper (Phase 8) may add native menus, dialogs, deep links, and file associations. **No feature may exist only in the wrapper**, and the wrapper may not reach into the database or the filesystem on the app's behalf. How the shell in `desktop/` keeps to that, and what it does at launch and at quit, is in [DESKTOP.md](DESKTOP.md); the protocol is [p8-notes/desktop-contract.md](p8-notes/desktop-contract.md).

### Desktop mode (Phase 8)

`SCRIOBH_DESKTOP=1` is the mode the wrapper starts the binary in. It adds, and only then:

- **The embedded issuer** (`internal/desktopidp`), mounted on the same listener at `/desktop-idp/`. The application is its one client and signs in through it with the code path it uses for any provider; `internal/server` knows desktop mode only in `desktop.go`, and the session middleware not at all.
- **The launch gate.** The wrapper's per-launch token is exchanged once, at `/desktop/start`, for a cookie; the issuer's authorization endpoint answers only that cookie, only as a navigation of the instance's own pages. This is what stands in for a password.
- **A Host check on every route**, against DNS rebinding.
- **One local profile**, made at the first run, kept in the `settings` table.
- **The identity-provider switch**: the instance can be moved to an external provider and back, at a restart, by proof and never by email address.

The package boundaries: `internal/desktopidp` depends on nothing of the server; `internal/domain/desktop` holds the state and the ops; `internal/config/desktop.go` the validation. The full protocol is [p8-notes/desktop-contract.md](p8-notes/desktop-contract.md).

---

## Search

- FTS5 virtual tables for documents, notes, entities, comments.
- Content is plain text extracted from Markdown (syntax stripped, wiki links expanded to titles).
- External-content tables with triggers keep FTS in sync inside the same transaction.
- Ranking: BM25 with field weights (title > synopsis > body), then recency as a tiebreak.
- Snippets use `snippet()` with markers rendered as `<mark>` server-side, escaped.
- Filters are applied as SQL joins, not post-filtering, so counts are honest.
- A 500,000-word project should search in under 50 ms on the target hardware.

---

## Realtime

- Every mutation writes a durable `events` row (`seq`, `type`, `project_id`, `doc_id`, `actor`, `payload`)
  in the same transaction as the content it describes (`Ctx.FlushTo`), so a change can never commit without its event.
- `GET /api/v1/events?since=<seq>` and the browser's `EventSource` both tail the table. The in-process hub is only a wake-up signal; replay and live are one code path, so a reconnect never loses or duplicates an event.
- Events carry ids, not prose. A browser that receives `doc.updated` re-fetches the document only if it is not currently being edited, and never while the editor has focus and unsaved input.
- Retention is configurable (default 90 days). Activity rows persist.

This is the Weave pattern, and it is worth copying exactly: it removes the "did the SSE fire before the transaction committed?" class of bugs.

---

## Interface language and text direction

Two separate things, both settled on the server so no script decides them.

**The interface** (buttons, labels, the words around the writing) comes from a message catalogue, `internal/i18n`: English is the source (`locales/en.json`, flat keys such as `rq.title` and `review.js.reply`), a translation is another file of the same keys, and a missing key falls back to English one string at a time, then to the key itself (so a gap is visible, never empty). A message may hold `{name}` placeholders. A person's language is a preference (`language`, set through `prefs.set` and Settings) and the page carries it as `Page.Lang` and `Page.UIDir`; templates call `{{$.T "key"}}` (root context) and a script is handed its strings as data (`{{json ($.Msgs "review.js.")}}` in `data-msg`), with the English default in the call (`T("reply", "Reply")`) so it still works if the data is missing. `<html lang>` and `dir` follow the language. A stranger's page (a share link) takes its language from `Accept-Language` and stores nothing. There is no service, no runtime download and no npm: adding a language is adding a JSON file and a line in Settings. **Applied to** the Phase 5 screens (review, the reading view's margin, suggestions, share links and the page a link opens, the role descriptions on the People page); the rest of the interface is English and is moved key by key as it is next touched, by replacing the literal with `{{$.T "area.key"}}` and adding the key. `TestEveryKeyTheScreensUseIsInTheCatalogue` fails when a template asks for a key the catalogue lacks or a script's English default differs from the catalogue.

Two pseudo-locales test the path without a translation and are offered in Settings (and to a stranger) only in a development build (the preference itself accepts them anywhere, where they only bracket English): `xx` accents every letter, pads and brackets each string (a string that is not in the catalogue, or is cut off, or does not fit its box, shows at once) and `xx-rtl` does the same and mirrors the page.

**The manuscript** has its own direction, from the project's language by the rule the DOCX and EPUB writers use (`publish.IsRTL`: ar, he, fa, ur, yi, ps, sd, ug, dv, ckb, ku), as `dir` on the page that shows the text (the writing view, the reading view; the compile preview and a share link's text when they are built), and `unicode-bidi: plaintext` on each block so a paragraph takes the direction of its own first strong character (a quotation in Latin inside Arabic reads correctly). The stylesheets use logical properties (`inline-size`, `margin-inline-start`, `inset-inline`) in the Phase 5 sheets. There is no separate per-document setting: a book is in one language; a paragraph that differs is set by the browser.

---

## Security controls

Non-negotiable, tested:

- **CSP:** `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'`. No inline scripts, no `on*` attributes, no `eval` (HTMX runs with `allowEval=false`). If an exception is needed, it is a hash and a comment.
- **CSRF:** origin/sec-fetch-site checks on every mutation, plus a token for forms. No state-changing GETs.
- **Headers:** `nosniff`, `Referrer-Policy: same-origin`, `X-Frame-Options: DENY`, restrictive `Permissions-Policy`, `Cache-Control: private` on HTML.
- **Body limits:** 2 MiB JSON/form default, explicit per-op overrides for uploads and imports; authorize before reading the body.
- **Uploads:** content sniffing, image decode guard (pixel cap, concurrency cap), no SVG served inline, no executables, filename sanitising, per-user quota.
- **Rate limits:** per principal token bucket, stricter buckets on login, search, export, and AI. `429` with `Retry-After`.
- **Secrets:** AI keys, OpenCode tokens, and Git credentials encrypted with AES-256-GCM using a key from `SCRIOBH_SECRET_KEY` or a `0600` key file. Never logged, never returned by an op, write-only in the UI. A missing key disables the provider with a clear message rather than storing plaintext. This is the *only* encrypted material: manuscripts, exports, the mirror, the database, and backups are not encrypted by the app.
- **Prompt injection:** manuscript content is untrusted data. Prompts delimit it and the system instruction says so. The OpenCode API is used only through its stateless generate and session text routes; Scríobh never calls its shell, PTY, filesystem, VCS, or MCP routes.
- **SSRF:** user-supplied provider URLs are refused unless the host is in an admin allowlist and is not loopback/link-local unless `SCRIOBH_ALLOW_PRIVATE_AI=true`.
- **Logging:** structured, no manuscript text, no prompts, no bodies. Request logs contain method, route, status, duration, principal id.
- **Container:** distroless, non-root, read-only root FS, `/tmp` tmpfs, capabilities dropped, `no-new-privileges`.
- **Backups:** contain the database, therefore the manuscript. They are plaintext; the operator protects the volume and the archive. The documentation says so plainly, and the UI never implies otherwise.

---

## Performance budgets

Enforced in CI where possible, checked by hand otherwise.

| Budget | Target |
| --- | --- |
| Server-rendered page, LAN | < 150 ms p95 for a chapter page |
| Editor interactive | < 250 ms after the editor chunk arrives, on a mid-range phone |
| Keystroke to paint | < 16 ms; no network in the path |
| Autosave round trip | < 400 ms p95; silent on success |
| Initial JS (gzip) | ≤ 40 KB shell, HTMX included; the editor bundle ≤ 180 KB, lazy-loaded on a document route |
| Editor interaction | first keystroke after chunk load < 250 ms on a mid-range phone; typing < 16 ms per keystroke |
| Initial CSS (gzip) | ≤ 25 KB for the sheets a page loads (the base sheet plus the page's own), measured per page by `TestCSSBudgetPerPage`; ≤ 96 KB across every sheet, as a cap on sprawl (raised from 40 KB at v0.3.5 when the sheets reached 39.9 KB, and from 64 KB in Phase 7 when the sheets reached 55.2 KB with about a dozen screens still to come; the per-page limit is the one that protects a reader and it is unchanged, and each new screen ships its own sheet, so the total grows with the number of screens, not with what any page loads) |
| Fonts | ≤ 4 files, subset, `font-display: swap`, self-hosted |
| Search, 500k words | < 80 ms p95 |
| Project open, 1,000 docs | < 400 ms |
| Memory, idle | < 120 MB RSS |
| Binary | < 40 MB (34.07 MB at Phase 6 print, of which 3.46 MB are the embedded fonts, the typesetter and the PDF writer; 30.60 MB before). Cover layouts (`golang.org/x/image/draw` and the package) add 0.47 MB to the stripped production binary on darwin/arm64 (41.75 to 42.24 MB there; the budget is measured on the Linux build, which is not run here) |
| Cover pictures | A cover picture is at most 24 million pixels (a 300-dpi Letter or A4 wrap is 19); a PNG over 16 million is refused with the advice to send a JPEG (the PDF writer decodes a PNG and caps it at 16 million; a JPEG goes in as it is); a progressive JPEG that Go's decoder would hold at more than 300 MB (15 bytes a pixel at 4:4:4, so over 20 million pixels) is refused with the advice to save a baseline JPEG. Measured through the real op path (peak resident set size of the process, 43 MB of it the process, Apple silicon): a 24 MP baseline 4:2:0 JPEG 109 MB, 4:4:4 147 MB; a 16 MP PNG 173 MB (RGBA), 283 MB (16 bits a channel); a 20 MP progressive 4:4:4 JPEG, the worst allowed, 370 MB; a rotated 24 MP baseline JPEG 273 to 309 MB; before the fix a 40 MP progressive JPEG took 1,045 MB and a 24 MP one 695 MB. All decodes in flight share a 600 MB budget whatever `SCRIOBH_COMPILE_CONCURRENCY` is, so covers hold at most about 750 MB at once. `cover.derive`: the front of a 300-dpi 6x9 wrap (3825 by 2775 px) is made, from nothing kept, in < 1.5 s p95 (`scriobh bench`; see COVERS.md "Image processing") |
| Cover page (C1 UI) | `cover.js` ≤ 5 KB gzip, lazy, on `/p/{slug}/cover` only (3.2 KB measured: a chosen or dropped picture submits the form (the first of several dropped files, said), drops outside a zone caught, the position grid's row keys, and the page-mode front redrawn as an SVG by a port of `coverlayout.PlacePage`); its sheet `cover.css` is 2.8 KB gzip, loaded after the base sheet on that page alone (13.4 KB of the page's 25 KB). The fixed Cover row adds 0.2 KB of `write.css` (the writing view's sheet: 22.3 KB of 25 KB) and 16 bytes of `tree.js`; the EPUB import's checkbox took `import-books.js` to 9.0 KB of its 9 KB limit, and dropping the English fallback of its six longest sentences (the catalogue always supplies them) brings it to 8.4 KB. The multipart form of the page goes through `uiRouteStream`: the caller is authorized before a byte of the body is read, the body is read a part at a time (never parsed whole), at most two cover bodies are read at once and one for each person (429 beyond), a body must keep moving (15 s waiting for a byte drops the connection, and the 15 s counts only the time spent waiting, never the server's own work between reads) and must average at least 32 KiB/s over the time spent waiting once it has waited 30 s (a 25 MiB picture at that rate takes 13.3 minutes, inside the ceiling; a sender who dribbles a byte every 14 s is dropped instead of holding a place), but may take up to 15 minutes in all, in place of the absolute 60 s `ReadTimeout` that would cut a slow uplink off and let a trickling sender hold a place, and its route has three times the upload limit; a 74 MB post by a non-member grew the heap by 0 MB (it was 223 to 257 MB), eight at once by 8 to 9 MB (it was 1.3 GB), eight members' by 116 MB (it was 1.2 to 1.5 GB); see COVERS.md. Enforced in the table in `internal/server/web_test.go` |
| Outbound requests at rest | zero |
| Phase 3 screens, server render | < 150 ms for the entity list at 5,000 entities (first page, alias search, a sheet), the research list and board at 600 notes, the outline in any order, and "Where this appears" for an entity named in 60 scenes; checked by `scripts/e2e-phase3.mjs` |
| Phase 3 screens, throttled phone (4x CPU, 390 px) | The entity list and the research list or board on screen < 2.5 s; "More" < 1.5 s; names from the bible underlined < 3 s after the editor mounts, typing unchanged (median key-to-frame under 16 ms at 1x); `mention.list` with spans and `scene.entities` < 50 ms |
| Lazy script files (gzip) | Each lazy file has its own enforced limit in the table in `internal/server/web_test.go`; from the round-two review fixes: `alts.js` ≤ 16 KB, `format.js` ≤ 3 KB, `doc.js` + `tree.js` ≤ 21.5 KB (21 KB until Phase 7: 21,682 B gzip measured after the passphrase screens, which add the writing view's hold through a project lock and its exit from a locked page, about 0.4 KB; the next round number up is 22 KB, which would leave three times the slack needed), `altpanel.js` and `stash.js` ≤ 5 KB each (the limit in the table; they are about 4 KB). A limit is raised only in the same commit that raises it here |
| AI screens, lazy script files (gzip) | The writing hot path does not grow: `aiboot.js` (≤ 1.25 KB, only when AI is on and the person edits) is the one AI file that rides with the page; `ai.js` ≤ 12 KB, `aipanel.js` ≤ 2 KB, `aichat.js` ≤ 3.5 KB, `aireviews.js` ≤ 5.5 KB and `lab.js` ≤ 8 KB are fetched when a control is first used (`lab.js` also with AI off, when the Lab tab is opened); `aiadmin.js` ≤ 11.5 KB loads on `/settings/ai` and `/p/{slug}/ai` only. Their sheets are `ai.css` (the two pages), `aiwrite.css` (writing view, with AI on or the Lab's accept-all) and `lab.css` (marks), each inside the per-page 25 KB. Enforced in the table in `internal/server/web_test.go` |
| Phase 5 lazy files | `review.js` ≤ 14 KB (the reading view for reviewers and up, and the Comments tab), `suggest.js` ≤ 7 KB (suggestion mode), `share.js` ≤ 8 KB (the page a share link opens), `share-links.js` ≤ 1 KB, `compile.js` ≤ 8 KB (the Compile page), `arm.js` ≤ 1.75 KB (a confirmation a double tap cannot get through, on the review, share, people and reading pages), `when.js` ≤ 0.6 KB (server times in the person's zone), `read.js` ≤ 3.5 KB; each page's own sheet (`review.css`, `share.css`) is inside the 25 KB per-page limit, and the writing view loads `review.css` only when the Comments tab is opened |
| Phase 6 publish and connect pages | `connect.js` ≤ 1.25 KB (the static site, webhooks and connect-an-agent pages: this server's own origin put in the snippets, copy buttons, focus on a secret shown once); those pages reuse `share.css` and `arm.js` and add `connect.css` (about 1.6 KB), inside the per-page 25 KB |
| Phase 7 mail pages | no script: the mail screens (`/admin/mail`, the Kindle address and digest on `/settings`, Send to Kindle on the Compile page, `/p/{slug}/kindle`) are plain server-rendered forms, so no lazy file and no `lazyJS` row; they add one sheet, `mail.css` (about 1.2 KB gzip), inside the per-page 25 KB |
| Writing Studio pages (Phase 7.5) | `studio.css` 3.2 KB gzip on the studio's pages only (13.7 KB with the base sheets, of 25 KB), `card.css` 0.5 KB on the public card page; the charts are server-rendered SVG and tables, so the dashboard needs no script. Lazy files: `sprint.js` ≤ 2.5 KB (2.3 KB; the writing view, fetched only while a sprint runs or when one is started from the Sprint menu; it rewrites one status-bar text when the minute changes), `card.js` ≤ 1.5 KB (the progress card page: the link shown once, copy, the PNG drawn on a canvas). The writing hot path does not grow: `doc.js` and `tree.js` are untouched, the writing view's CSS grows by 29 bytes gzip to 21,961 (the menu's number box), `shell.js` by 0.2 KB (a form that names a script is handed to it on its first submit) and `palette.js` by 0.1 KB. |

| Phase 7 screens: publish | `publish.js` ≤ 1.25 KB gzip (the release page only: follows an edition being built and uploaded by replacing its own fragment; 0.9 KB measured); `publish.css` (0.7 KB gzip) is loaded on the publish and release pages after `git.css`, inside the per-page 25 KB. The Git page grew by the remotes list, the audience, signing and trigger parts (`git.css` 2.0 KB gzip), and `git.js` stays under its 2 KB limit (1.5 KB). |
| Import page | `import.js` ≤ 8 KB (read a folder, zip or Word document, the preview tree, edits, apply, take back a refused change, retry when busy, move to the ends of a list, announcements, the rules that keep a join and a left-out chapter apart), `import-formats.js` ≤ 3.5 KB (the format chooser, Scrivener, WordPress and Ghost sources, the research notes and the converter's report) and `import-books.js` ≤ 9 KB (EPUB and PDF sources, the book, fidelity and tracked-changes panels, nest and join, the answer after an apply that kept an edition; its words are in the catalogue, `imp.js.*`), all on the import pages only, with `import.css`; the Editions pages and their diff and compare screens are server-rendered, with no script, and load `editions.css` (about 1.9 KB gzip) only |
| Phase 7 Pandoc screens | No new script and no new stylesheet: the administrator's Pandoc page is plain forms with `admin.css` (about 1 KB gzip); the Pandoc panel on the import pages is `import-formats.js` (3.4 KB of its 3.5 KB limit, measured 3,449 bytes gzip) and the Compile page's format gating is `compile.js` (6.8 KB of 8 KB) |

| Phase 5 lazy files | `review.js` ≤ 14 KB (the reading view for reviewers and up, and the Comments tab), `suggest.js` ≤ 7 KB (suggestion mode), `share.js` ≤ 8 KB (the page a share link opens), `share-links.js` ≤ 1 KB, `compile.js` ≤ 8 KB (the Compile page), `compile-print.js` ≤ 5 KB (the Compile page's print proof, printer's checklist and paperback cover, Phase 6), `arm.js` ≤ 1.75 KB (a confirmation a double tap cannot get through, on the review, share, people and reading pages), `when.js` ≤ 0.6 KB (server times in the person's zone), `read.js` ≤ 3.5 KB; each page's own sheet (`review.css`, `share.css`) is inside the 25 KB per-page limit, and the writing view loads `review.css` only when the Comments tab is opened |
| Phase 7 screens | `gate.js` ≤ 4.5 KB (every page of a project that has a passphrase, once it is open: the lock itself, the cover, the page's own idle timer, the way to the unlock page, Lock now; it is not loaded for a project with no passphrase), `held.js` ≤ 3.5 KB (the same pages: a local draft of every field that is not saved yet, put back after an unlock), `jump.js` ≤ 1 KB (the writing view opened at a passage); the sheets `gate.css` (about 1 KB), `privacy.css` (about 1.3 KB) and `cmanage.css` (about 0.8 KB) are each inside the 25 KB per-page limit (the writing view's total is about 23 KB with `gate.css`) |
| Live updates client | `live.js` ≤ 2 KB gzip, lazy; stops while the page is hidden and resumes from the last event id |

No web font is loaded until the first paint of text. No icon library: a small inline SVG sprite. No polyfills beyond what the last two versions of Safari and Chrome need.

---

## Large manuscripts and SQLite

**SQLite is not the bottleneck at manuscript scale.** A one-million-word manuscript is roughly 6–7 MB of UTF-8 text. Add 1,000 documents, 20,000 revisions, 5,000 entities, 200,000 mentions, and a 90-day event log and the database is in the hundreds of megabytes, not gigabytes. FTS5 indexes a million words in seconds and answers queries in milliseconds. The target hardware is a mini-PC or a Raspberry Pi 5; both are comfortable.

The real limits are elsewhere:

| Risk | Why it bites | Mitigation |
| --- | --- | --- |
| One gigantic document | A rich editor with 100,000 words in a single document is slow, regardless of database | Chapters and scenes are the unit; warn at 15,000 words, suggest a split, and switch to a read-only safety mode beyond 25,000 |
| Revision growth | Full snapshots every few minutes can dwarf the manuscript | Compress snapshots, deduplicate by content hash, prune autosaves, keep manual snapshots forever |
| First FTS build | Indexing a large imported book can take a while | Build in the background with progress; the UI stays usable |
| Huge exports | A 500-page PDF or DOCX can be memory-hungry | Stream the writer, bound memory, and cap the PDF page count with a clear message |
| Pure-Go SQLite | `modernc.org/sqlite` is slower than the CGO driver in write-heavy workloads | Benchmark first; keep a build-tagged `mattn/go-sqlite3` escape hatch if the numbers miss |

### Pragmas

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
PRAGMA cache_size = -8192;       -- 8 MB, on the writer and on each of the four readers
PRAGMA mmap_size = 268435456;    -- 256 MB
PRAGMA temp_store = MEMORY;
PRAGMA wal_autocheckpoint = 1000;
```

The page cache is small on purpose, and the same on every connection. A connection keeps every page it has read or written until its cache is full, and under the pure-Go driver each cached page costs several times its own bytes in the driver's allocator (see "Soak" below), so the writer's cache, not the readers', is what grows with the database: with 64 MB it took the process hundreds of megabytes above the Go runtime's own over an hour of writing. The 256 MB memory map serves the reads, which is why the four readers' caches stay almost empty. `TestPageCacheStaysWithinItsLimit` holds the limit; do not raise it without a soak. The bench's operations are indifferent to it: with 64, 8, 4 and 2 MB (median of three runs each, one after another) saves, searches, pages, the search rebuild, backups and the mirror write at 150k and 500k words gave the same times within the run-to-run spread. The choice of 8 MB over 2 is a margin for what the bench does not time (a bulk import or a restore, which writes many pages in one transaction and spills past a small cache, correctly but slower), and it costs about 20 MB more of the allocator's memory at its plateau (37 MB against 16 MB in the experiments below).

`PRAGMA optimize` runs at startup and on clean shutdown. Autosave is one transaction per debounce window, never one per keystroke. One writer, one replica; that is the design, not a limitation to work around.

### Budgets at 500,000 words, 1,000 documents

| Operation | Budget |
| --- | --- |
| Open a chapter | < 150 ms p95 |
| Save a chapter | < 120 ms p95 |
| Full-text search | < 80 ms p95 |
| Semantic search, 10,000 chunks of 768 dimensions, after the question is embedded (`search.semantic`) | < 80 ms p95 |
| FTS rebuild | < 60 s |
| DOCX compile, 150k words | < 10 s |
| DOCX compile, 500k words | < 30 s |
| Database size | < 1 GB with pruned history |
| Backup | < 30 s |
| EPUB import, 300 pages with pictures | < 60 s |

`scriobh bench` generates 150k, 500k, and 1M-word projects and measures each operation, plus database size. It runs nightly in CI with a published report; a regression over 20% fails the build. The numbers are not aspirational: they are the gate for calling a release done. How to run it, what it measures and what fails it: [OPERATIONS.md](OPERATIONS.md#performance-scriobh-bench); the numbers measured are in [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) ("Phase 3 platform decisions").

**Soak.** `scriobh bench --soak 20m` (`make soak`) serves the generated project on a loopback socket with every background worker running (the mirror, the Git and webhook queues, and the hourly sweeps, which first run an hour in) and keeps a writer and a reader busy for the whole duration: a save every ~50 ms on eight chapters and scenes, a search after each, a page or a document every ~100 ms, an alternative made and deleted every 15 s, a compile preview of the whole book every 30 s, a Markdown bundle export every minute, a DOCX compile and a snapshot every 2 minutes, and a backup every 5 minutes (removed after). Every 5 s it samples RSS, the process's footprint (dirty and swapped pages; reported, not judged), the live heap after the last collection, the memory the Go runtime holds from the system, goroutines, open files, the WAL and the database. The rule: after the warm-up (a sixth of the run, 30 s to 5 minutes), the median of the last third may not exceed the median of the first third by more than max(25%, 32 MB) for RSS, max(25%, 16 MB) for the heap, max(25%, 32 MB) for the Go runtime's memory, max(10%, 10) for goroutines, max(10%, 8) for open files, max(50%, 16 MB) for the WAL and max(25%, 32 MB) for the database (which grows by design: one event per save, an autosave revision per document every five minutes, snapshots and export records); and every budgeted operation must keep its p95 within the budget above over the run and over its last third. Measured on 2026-11-14 on a laptop (darwin/arm64, 16 cores, Go 1.27.4) with browser tests from other work running alongside:

| | 150k words, 296 docs, 22 min | 500k words, 989 docs, 12 min |
| --- | --- | --- |
| RSS, first third → last third (max) | 270.8 → 292.5 MB (303.5) | 359.3 → 369.3 MB (376.9) |
| Live heap | 86.1 → 86.6 MB | 87.8 → 87.2 MB |
| Goroutines / open files | 23 → 23 / 20 → 20 | 23 → 23 / 20 → 20 |
| WAL | 4.2 → 4.2 MB | 5.1 → 5.1 MB |
| Database | 8.4 → 12.9 MB (14.0) | 18.2 → 19.9 MB (20.8) |
| Saves over HTTP (p50 / p95, budget 400) | 21,887: 2.9 / 6.1 ms | 11,654: 3.7 / 7.5 ms |
| Searches (budget 80) | 21,887: 5.4 / 9.8 ms | 11,654: 4.8 / 8.5 ms |
| Chapter page (budget 150) | 11,346: 12.4 / 17.6 ms | 5,554: 24.5 / 28.7 ms |
| Project page (budget 400) | 1,135: 9.8 / 11.3 ms | 556: 20.1 / 23.3 ms |
| Open a document (budget 150) | 11,346: 0.8 / 1.2 ms | 5,554: 1.4 / 1.9 ms |
| DOCX compile (budget 10 s / 30 s) | 10: 51.7 / 56.5 ms, 428 KB | 5: 129.1 / 140.1 ms, 1.4 MB |
| Compile preview, whole book | 43: 40.4 / 42.7 ms | 23: 108.1 / 112.5 ms |
| Backup (budget 30 s) | 5: 111.6 / 126.2 ms | 3: 226.1 / 233.9 ms |
| Alternative made and deleted | 87: 11.1 / 19.6 ms | 47: 8.4 / 12.4 ms |
| Revisions written / errors | 218 / 0 | 117 / 0 |

In those two runs no gauge grew past its margin and every budget held, with no failed operation. The live heap, the goroutines and the open files are flat. **RSS was not flat, and a longer run failed the rule; the cause was the writer's page cache and it is fixed (2026-11-14).** A 65-minute run at 150k words (64,409 saves, 642 revisions, 13 backups, 32 DOCX compiles; every budget held, p95 of a save 6.2 ms) ended with RSS at 358.0 MB against 279.9 MB in its first third, over the allowed 349.9 MB, rising 1 to 3 MB a minute in every sixth of the run. A later run, with the readers' caches already cut to 8 MB, climbed from 250 MB to 416 MB in 63 minutes while the memory the Go runtime holds stayed at 230 to 237 MB, the live heap at about 90 MB, goroutines and open files flat, and the database grew from 6 to 31 MB: about 180 MB of RSS outside the runtime, 2.5 to 3 MB for each MB the file grew.

The first explanation, "every connection's page cache fills, bounded by 5 x 64 MB", was incomplete and the reader cut did nothing. What the experiments showed, each over 10 to 15 minutes of the soak's load at about five times its save pace (a 5 ms pause instead of 50 ms, so the file grows about 13 MB in 10 minutes), four runs at a time on a busy laptop, so the runs are compared with each other. RSS and the process's footprint (dirty and swapped pages, without clean file pages) are in MB per 10 minutes:

| Change | RSS | Footprint |
| --- | --- | --- |
| Nothing (two runs, file +13 MB each) | +63, +72 | +64, +62 |
| Saves only / pages and searches only (no writes) | +53 / +4 | +46 / +2 |
| Memory map off (`mmap_size 0`) | +92 | +91 |
| `temp_store FILE` | +67 | +65 |
| One reader connection instead of four | +62 | +56 |
| The writer connection closed and reopened every minute | +36 | +35 |
| `PRAGMA shrink_memory` on the writer every 30 s | +37 | +30 |
| Writer cache 2 MB | +11 | +7 |
| Writer cache 8 MB | +43 | +25 |
| With a backup every 45 s for 15 minutes (file +20 MB): writer cache 2 / 4 / 8 / 16 MB | +15 / +19 / +12 / +42 | +7 / +6 / +8 / +38 |

- Only writing grows it; reading does not (pages and searches: flat).
- The four readers' caches hold about 0.4 MB between them (SQLite's own count of cache bytes in use, sampled every 15 s), because the 256 MB memory map serves their reads; with the map off the growth is larger. Only the writer's cache fills, with about the pages the file gains (1.1 MB for each MB of file).
- Each cached page costs several times its bytes in the allocator under the driver (the allocator's own count of memory it asked the system for grew 3.5 to 4.5 MB for each MB of file, and stopped at 71 MB with a 16 MB cache and 37 MB with 8 MB). Its size classes are powers of two, which we take to mean an 8 KB slot for a 4 KB page and its bookkeeping; the count of slots was not measured, only the totals.
- That memory is anonymous, not clean file pages: the footprint rises as much as RSS, and the mapped database contributes only 1 to 8 MB. RSS is therefore the right quantity for the growth rule; the soak now also samples the footprint (on Linux, `RssAnon`) and reports it beside RSS without judging it.
- Recycling the writer connection or `shrink_memory` halves the slope and does not stop it, probably because the allocator retains freed regions (its documentation says up to the high-water mark of its live memory plus 4 MB) and a refilled cache lands in a fragmented heap; that reason was not tested. A smaller cache does stop it. It is the cap on what the writer may hold that bounds the process, so the cap is the fix.
- The cache size does not show in the bench: `scriobh bench --sizes 150k,500k --runs 15` with the writer cache at 64, 8, 4 and 2 MB, three runs each one after another, gave the same medians (150k words: save p95 6.5, 6.1, 6.3, 6.3 ms, search rebuild 39, 38, 41, 40 ms, backup 79, 78, 80, 79 ms; 500k words: search rebuild 109, 102, 105, 111 ms, backup 203, 207, 206, 207 ms). A first set of runs made three at a time showed a 500k-word rebuild at 2 MB 2.7 times as slow; it did not repeat and was noise from running them together.

With one 8 MB cache on every connection, a 30-minute soak at 150k words side by side with the old 64 MB writer cache: RSS 279.8 to 285.5 MB (first to last third) against 277.5 to 309.0 MB, footprint 236 to 247 MB, RSS slope 6.6 MB per 10 minutes against 19.0 (footprint 5.6 against 17.8). The remaining slope is the 8 MB cache filling. The same load for the length that failed, 65 minutes at 150k words (66,764 saves, 641 revisions, 13 backups, 32 DOCX compiles; the database 6 to 30 MB; every budget held, p95 of a save 5.1 ms): RSS 289.7 MB in the first third, 302.5 MB in the last (allowed 362.1; the failing run: 279.9 to 358.0), footprint 243 to 249 MB, the runtime's own memory 206.8 to 211.1 MB. Over the whole run after the warm-up the slopes are RSS +3.9, footprint +1.7 and the runtime's own memory +1.0 MB per 10 minutes, while the file grew 3.7 MB per 10 minutes throughout. The slope falls as the cache fills: RSS +10.8, +2.2 and -1.7 MB per 10 minutes over the first, second and last thirds (footprint +7.1, +2.0, -0.9). The footprint less the runtime's memory, which is what SQLite's cache and the allocator hold, went from 27 MB at 3 minutes to 34 at 8 and 37 to 39 MB from 13 minutes to the end (file 11 to 30 MB over that stretch); a rise of about 8 MB in RSS, footprint and the runtime's memory together near the 45-minute mark is the Go runtime's. RSS less the footprint (clean pages, the mapped file and the binary) wandered between 36 and 60 MB with no trend. At 500k words, 20 minutes (too short to call a plateau): RSS 362.3 to 370.4 MB (allowed 452.9; before the change, with only the readers cut, 403.8 to 442.3), footprint 247 to 255 MB, every budget held (save p95 6.4 ms, search 7.1 ms, chapter page 24.7 ms in the last third). These soaks ran on the same laptop with other work running, and are the record of this change; the table above is that of the first two runs, before it.
The numbers are those of the bench process, which holds the server and the load generator together. The database grows by design under this load (about 20 MB an hour at 18 saves a second, far above a person's pace): mostly the event log, one row per save. The first run found that nothing ever removed old event rows, against a documented 90-day log; the hourly sweep now does (its first tick fell inside the 65-minute run, with every row younger than 90 days and so nothing to remove; the bench discards the server's log, so that run does not show what it did, and the deletes themselves are covered by a test). Export records (a manifest of about 35 KB for each compile or export) are never removed; each export records one by design, and no retention is decided for them.

### If SQLite ever is not enough

The escape hatch is deliberately narrow:

1. **Driver:** switch to CGO SQLite behind a build tag if the pure-Go driver misses the budgets by a wide margin. Same schema, same queries.
2. **Search:** if FTS5 is the only limit, put Bleve or Tantivy behind the existing search interface. The database stays.
3. **Postgres:** only if the product grows multiple writers or real multi-tenant hosting. That is explicitly out of scope, and adopting it early would cost the single-binary deployment for no current benefit.

The plan does not pre-emptively trade the deployment story for a scale it does not need. It measures, then decides.

---

## Testing

| Layer | What |
| --- | --- |
| Unit | Markdown parse/serialize, numbering, diff, word counts including ghosting, context building, cost maths |
| Editor | Markdown round-trip (`parse(serialize(doc))`), paste sanitising, IME input, undo/redo, Source toggle |
| Op tests | Every op against a temp SQLite DB with real principals: happy path, forbidden, invalid, conflict |
| Auth | The stub OIDC issuer: login, PKCE, state/nonce, group mapping, expiry, and revoke |
| Authz sweep | Anonymous, outsider, reader, reviewer, editor, owner, agent across every op; asserts 404 for missing access |
| Golden files | HTML fragments, exported Markdown/HTML/EPUB manifests |
| E2E | Playwright or Chromium CDP: sign in (stub IdP), write, save, reload, export |
| Performance | `scriobh bench` on 150k/500k/1M-word projects: open, save, search, compile, database size; nightly report; a 20% regression fails |
| Skill | `go test ./cmd/scriobh-skillgen` regenerates the skill and CLI snapshot in memory and fails when the committed files differ |
| CSP smoke | Walk every page in Chromium, fail on any console violation |
| Contrast | Script over every theme and accent combination in CI |
| Migration | Apply from empty, apply from the previous release's fixture, assert data intact |
| Backup/restore | Round-trip a populated data directory into a temp dir and verify hashes |
| Performance | A generated 150k-word project; assert page and search budgets |

A stub OIDC provider for tests (a local server that signs tokens) keeps CI independent of an IdP.

---

## Configuration

Environment first, DB overrides second, UI last. Secrets only from env or a key file.

```
SCRIOBH_BASE_URL=http://localhost:8080   # set to the public URL behind your proxy
SCRIOBH_LISTEN=127.0.0.1:8080             # the image sets :8080; use 127.0.0.1:0 for an ephemeral port
SCRIOBH_PORT_FILE=                        # optional: write the bound port here once listening
SCRIOBH_DATA_DIR=/data
SCRIOBH_OIDC_ISSUER=https://auth.example.com/application/o/scriobh/
SCRIOBH_OIDC_CLIENT_ID=...
SCRIOBH_OIDC_CLIENT_SECRET=...
SCRIOBH_OIDC_STUB=0                # dev only; refused unless base URL and listen are loopback
SCRIOBH_DEV_ALLOW_ANY_LISTEN=0     # dev compose only; see "Development identity provider"
SCRIOBH_ADMIN_GROUP=scriobh-admins
SCRIOBH_WRITER_GROUP=scriobh-writers
SCRIOBH_READER_GROUP=scriobh-readers
SCRIOBH_SECRET_KEY=...            # at least 32 characters; session + AI credential encryption
SCRIOBH_SESSION_HOURS=24
SCRIOBH_SESSION_IDLE_MINUTES=480
SCRIOBH_AI=on                     # off: the kill switch, every AI op that could send refuses and no provider is contacted
SCRIOBH_AI_USER_PROVIDERS=on      # off: only administrators add providers
SCRIOBH_AI_MAX_REQUEST_KB=256     # the most one request sends; see OPERATIONS for the other SCRIOBH_AI_* limits
SCRIOBH_RATE_LIMIT=600
SCRIOBH_LOG_LEVEL=info
SCRIOBH_GIT_BINARY=               # not built: go-git only
SCRIOBH_GIT_MAX_REPO_MB=512       # refuse larger clones
SCRIOBH_GIT_ALLOW_PRIVATE=0       # 1: let Git sync reach loopback and private addresses (a LAN Forgejo)
SCRIOBH_SITE=on                   # off: no static site preview, build or publish
SCRIOBH_SITE_DIR=                 # an absolute folder static sites are published under; empty: publishing to a folder is off
SCRIOBH_SITE_PUBLISH=0            # 1: publish under <data>/sites when SCRIOBH_SITE_DIR is empty
SCRIOBH_GIT_USER_QUOTA_MB=2048    # one person's repository imports; 0 = no quota
SCRIOBH_GIT_TOTAL_QUOTA_MB=20480  # everything Git sync keeps; 0 = no quota
SCRIOBH_GIT_BUNDLES_KEEP=5        # bundles of overwritten history kept per remote
SCRIOBH_GIT_KEEP_IMPORTS_DAYS=30  # how long a kept import clone lives; 0 = forever
SCRIOBH_MAX_ALTS_PER_DOC=200      # alternatives per document
SCRIOBH_DESKTOP=0                 # 1: desktop mode, the sidecar of the desktop shell (needs SCRIOBH_LAUNCH_TOKEN)
SCRIOBH_LAUNCH_TOKEN=             # desktop mode only: the shell's secret for one launch
```

The CLI reads its own two variables, not the server's:

```
SCRIOBH_URL=https://write.example.com
SCRIOBH_API_KEY=scr_...            # `scriobh-cli login` saves both in config.json, 0600 (SCRIOBH_TOKEN is accepted for the key)
```

`scriobh config check` (the server binary) validates the whole set and prints what is missing, like `weave` does; `scriobh-cli config` shows where the CLI's server and key come from, never the key.

### TLS and the reverse proxy

There is no built-in ACME and no built-in TLS. The administrator runs the reverse proxy and owns the certificates; the app is a plain HTTP server behind it.

- `SCRIOBH_BASE_URL` defaults to `http://localhost:8080` and is the single source for absolute links, OIDC callbacks, and secure cookies. Set it to the public URL.
- `SCRIOBH_TRUST_PROXY` is `false` by default. When the app is behind a trusted proxy, the proxy sets `X-Forwarded-Proto` and the flag can be enabled; otherwise secure cookies follow the base URL's scheme.
- A container deployment listens on `:8080`; a desktop or bare-metal deployment listens on `127.0.0.1`.
- The docs give Caddy, Traefik, and nginx examples, including the headers the app expects.

---

## Failure modes, and the story for each

| Failure | Behaviour |
| --- | --- |
| IdP unreachable | Existing sessions keep working until expiry; new logins show a clear error with the issuer URL. No local bypass. |
| Database locked | `busy_timeout` 5 s, short transactions, retry once, then a visible "try again" toast. No data loss. |
| Disk full | Writes fail before truncation; save returns a hard error and the client keeps the text in the editor buffer and local queue. Never a silent partial file. |
| Mirror write fails | Logged, retried, never blocks a save. The DB remains canonical. |
| AI provider down | Typed error in the drawer; one retry, then the configured fallback chain (never past a refusal); a call that reached the provider is charged what was sent. |
| Headless browser missing | Print PDF explains what to install; HTML print still works. |
| Corrupt FTS index | Rebuild command; search degrades to `LIKE` with a banner. |
| Upgrade interrupted | Migrations run in one transaction per file; a pre-flight backup is taken automatically. |
| Git remote diverged | Refuse the push, show the remote commits, require an explicit force with a local bundle first. |
| Git auth fails | Typed error with the host and key name, no retry storm, "Test connection" in settings. |
| Git remote unreachable | Keep the commit locally, mark the remote as behind, retry on the next trigger. |
| Clone is too large or has no text | Refuse before writing; show the limit and the file count. |
| Embedded OIDC port conflict (desktop) | Cannot happen: the embedded issuer is served on the application's own listener, under `/desktop-idp/`. The wrapper reads the one port from the `LISTEN` line. |
| A second desktop instance on the same data | Prints `ALREADY_RUNNING`, exits 75; the wrapper brings the first window forward. |
| Wrapper cannot reach the server | Native error with the log path and a Retry; never a blank window. |
| Alternative anchor lost after a large edit | Orphan it, keep every wording, list it for review. Re-attach only by an exact, unique match of its text and context, or by `alternative.reanchor`; never by a guess. |
| Lab output malformed or truncated | Show it raw in the scratchpad; apply nothing. |
| Export comment unreadable on import | Keep the prose, warn, import with no variants. |
