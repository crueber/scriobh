# Phase 7: the project passphrase (F-252) and Compendium management (F-478)

Backend and ops only; the screens come later. Section letter AT in FEATURES, ids F-252, F-478 and F-870 to F-881, migrations 1850, 1851 and 1852, i18n prefixes `project.gate.` and `compendium.manage.`.

## 1. The project passphrase

### What it is, and what it is not

An application-level gate for a project. While a project has a passphrase, a signed-in member must unlock it in their browser session before anything of the project is served. **It is not encryption.** The text is in the database as it always was.

It keeps out:

- a person at a shared screen;
- a borrowed laptop that is signed in;
- a family member using your session;
- a beta reader or an editor who has the project but not the passphrase (the passphrase is the owner's to share).

It does not keep out, and the UI says so in its own words (`project.gate.notice`, and `projectgate.Notice` for REST and CLI answers):

- the server's administrator and anyone with the database file;
- backups;
- the Markdown folder the mirror writes;
- Git pushes;
- exports an unlocked session makes;
- a share link an unlocked owner made earlier (it has its own optional passphrase and is not touched by the project's: see below).

The words, as shown: "This passphrase locks the project on this screen. It keeps a shared screen, a borrowed laptop or someone using your session out. It is not encryption: the server's administrator, the database file, backups, the Markdown folder, Git pushes and exports still hold your text as it is."

What a locked project shows: its title and address, the fact that it needs a passphrase, and the person's own state (role, pinned, archived, trashed). Nothing of the text, the titles of documents, search hits, backlinks, comments, revisions, notes, descriptions, dates, or counts (`project.list` gives a locked project as `locked: true` with those fields empty).

### How it fails closed

The gate is asked in the places every project-scoped read and write already passes, so a route or op that forgets it is not possible:

1. **`authorizeInput` in `internal/ops/auth.go`**, for every op that names a project (or a document, comment, tag, attachment or anything else that belongs to one): after the role check, before the trash check. A stranger still gets the 404 a missing project gives, with or without a passphrase. The exempt list is `projectGateOK` in `internal/ops/gate.go`: `project.passphrase.get/set/clear`, `project.unlock`, `project.lock`, and nothing else. `project.get` is gated, so every page that loads a project through it is closed too. A test pins the list at five.
2. **`Env.ProjectRole`**, the function every hand-written role check goes through (the Compendium ops that read a project's text, AI, review, Git, mentions, the timeline), is gate-aware (`ops.GateRole`): for a caller found in the context who asks about their own role, a project the gate refuses answers "no access". `Env.ProjectMember` is the same without the gate, for the gate itself and the ops that answer a locked caller. The caller is carried in the context (`ops.WithPrincipal`, set by `Registry.Call`, the dry-run path and the HTTP layer). A context with no caller is background work, and the gate is not for it.
3. **The page layer** (`internal/server/gate.go`) resolves a request's project from its address (`/p/{slug}…`, `/ui/projects/{id}…`, `/ui/docs/{id}…`, `/files/{id}`, and the REST shapes `/api/v1/projects/{id}…` and `/api/v1/docs/{id}…`) and asks the same function before any handler runs, so a route written by hand under those addresses, which the registry does not see, is closed too (`TestGateClosesAHandWrittenRouteUnderAProjectAddress`). A key pinned elsewhere is skipped, and left to the op layer's 404, so the gate never names another project to it. A person at a page is sent to `/p/{slug}/unlock?next=…`; a fragment or a script gets the 403 JSON; an htmx request gets `HX-Redirect`. The unlock, lock and passphrase routes are the only exemptions (`gateExemptPage`).
4. **The lists that span projects** (`project.list`, `notification.list/count/mark_read/read_all`, a Compendium's views of the books that use it, the project search page) go through 1 and 2 or filter explicitly (`projectgate.LockedProjects`).

The registry builds all of it (`registry.BuildWith`); a test fails if the environment has no gate.

**The sweeps** (all in `internal/server`, all with a positive control that must find the canaries as the owner who unlocked it, or the locked walk proves nothing):

- `TestGateOpSweep`: every registered op as an editor, the owner's second session and a non-member administrator, none of whom has unlocked. A project op must answer 403 `project_locked`; no read of any kind may carry a canary or a count. Canaries sit in the document title and body, a second chapter, a comment and its reply (a notification for the editor), a snapshot label, a stash item, a research note and tag, an attachment name, a share link label, a book override, the project's subtitle and description, and the word counts.
- `TestGateAggregatesDoNotLeak`: the library, notifications, a Compendium's entity, timeline and search views, the global search page, sessions.
- `TestGatePageSweep`: every project-scoped GET and form route found in the source (a mux cannot list its patterns, so the test parses the server's source for literal patterns) as a locked editor and the owner's other session; also a non-member administrator.
- `TestEveryRouteIsClassifiedForTheGate` fails closed. Every route the source registers is either under a project address (and then walked by `TestGatePageSweep`) or listed in `notProject` by its **exact pattern** with a reason: there are no prefix exemptions, not for `/api/`, `/admin` or `/c/{slug}`. A registration whose pattern is not a literal `METHOD /path` (the research routes built from `page` and `ui`, the share loop, the op loop, `/mcp`) is expanded where the test knows the values, and otherwise must be listed by call site in `dynamicSites`. A route listed with `walk: compendium` or `walk: admin` is walked by `TestGateCompendiumPagesAreSwept` and `TestGateAdminPagesCarryNothingOfAProject` (listed means walked, forms included), so the administration pages are shown, not asserted, to carry nothing of a project. A hand-written `GET /api/v1/docs/{id}/anything` is gated by its address, or fails the test. (Writing this walked the research routes of a project, which the first sweep had missed because their patterns are built from variables.)
- Mutations I made and reverted to see the sweeps fail: the role function without the gate (the sweep named `entity.appears_in` and the timeline reads), a missing gate in the op layer.

### Decisions

**(a) What it protects against.** Above. The threat is a person at the screen, not the server.

**(b) How an unlock lives.** A row in `project_unlocks` keyed by (session, project), made by `project.unlock` with the passphrase, in the epoch the gate is in. It ends:

- after the project's idle limit (default 30 minutes, set by the owner, 1 to 720; separate from the session's own idle limit), counted from the last real use. A use writes to the database at most once a minute, from a goroutine, so a read never waits for the single writer; in between the clock moves in memory;
- at 12 hours from the unlock whatever the use (an open tab must not stay open all day);
- on demand (`project.lock`, session only), in this session only;
- when the session ends (the gate checks the session is live, so a revoked session's row opens nothing; the row goes with the session when swept);
- when the passphrase changes: the gate's `epoch` moves, every other row is void and deleted, and the session that changed it stays open.

A new sign-in is a new session and is locked. A stream of live updates (`GET /api/v1/events`) does not count as use: its half-minute recheck is passive (`ops.Passive`), it ends with `bye` **at once** when its own session locks the project, the passphrase changes or is removed (the gate signals the streams watching that project, which ask again), the gate is asked for every project event as well as at the heartbeat, so an idle expiry or another relock stops it at the next event, and it cannot keep an unlock alive (`TestGateStreamEndsAtOnceOnLock`, `TestP7ReviewStreamDeliversNothingAfterLock`).

**(c) API keys.** A key is its own credential and does not need the passphrase, so the choice is the owner's, with the safe default: **keys are refused while a passphrase is set** (`allow_keys` false; 403 with `data.reason` `project_keys_refused`). Reasons for the default: the threat is a person at a screen, and a key left on a laptop or in an agent's store is exactly a credential that outlives the screen. **Accepting keys is per key, not per project.** With `allow_keys` on, a key still passes only if it is approved for this passphrase: it was made by a session that had the project unlocked (approved by being made), or the owner approved it by id (`project.passphrase.set` with `approve_keys`, needing the passphrase; `unapprove_keys` withdraws one). A key an editor made earlier, when they could not have had the passphrase, is never let in by the owner switching keys on (`TestGateKeysArePerKeyApprovals`, `TestP7ReviewEditorKeyMadeEarlierDoesNotBypassTheirMissingPassphrase`). The owner is offered their own live keys and the keys that have tried the project (a member asks their agent to try once; the refusal puts the key in the list); approvals carry the passphrase's epoch and a new passphrase voids them all, with the unlocks (they are in `project_gate_key_ok`, migration 1852). A key not approved is treated as locked everywhere: refused by the ops, and the lists (`project.list`, notifications) redact as for a locked session. Every call of a key at a protected project is counted per key (`project_gate_key_use`: calls let through, calls refused, first and last time), shown to the owner in `project.passphrase.get` and to the key's owner in `key.list` (`protected_projects`). One call is one count: the registry's check counts, the early authorization a binder makes before it reads the body (`ops.Precheck`) counts only a refusal, the role checks made by hand inside the op do not (they are passive), and neither does a stream's recheck, so a key's call is never counted twice (`TestGateKeys` pins exact numbers). An op that checks the role only by hand (the AI budget read with a project) is not counted. The counts are kept in memory and written at most once a minute per (key, project) and at shutdown.

A key can neither set, clear, unlock nor lock (`Auth.ForSessions`), and **cannot be made by a person who has not unlocked what it would reach**: `key.create` with a project pin needs that project unlocked, and a key with no pin (which reaches every project its owner is on) needs every protected project that accepts keys unlocked (`projectgate.CheckMint`); a key made by a key is never approved. Even without that refusal such a key would not pass (it is not approved); the refusal is for a clear answer. Projects that refuse keys are not asked about: a key is refused there whoever made it. Compendium-pinned keys reach no project.

**(d) Background work.** The design: mirror, webhooks, Git pushes, the mention and link index workers, search indexes, backups and an AI run that is already running do not ask the gate. A context that names no caller is not gated, and none of those workers is given one. **Tested:** the mirror (`TestGateBackgroundWorkIsNotGated` rebuilds a locked project's mirror with a caller-less context and finds the text), the mention index (a move bumps stamps and a `mention.rebuild` as the owner proves the rows), the operator's shell. **Not tested:** webhook delivery, a Git push and a running AI run while a project is locked; they do not call `Env.ProjectRole`, which is how the gate would reach them (by reading), and nothing I changed touches them. Starting an AI run is a person's act and needs the project unlocked, like any op. The operator's own shell on the server (the import command, principal `Via: "cli"`) is not a person at a screen and passes.

**(e) The passphrase is never stored or logged in the clear.** Argon2id at the OWASP minimum (the share links' parameters, now in `internal/passkit`, which `share` uses too: one hash, one verifier, one schedule). 8 to 200 characters. Constant-time compare. The ops that carry one are `SecretInput`: never previewed (a rolled-back wrong guess would be a free guess), and an `Idempotency-Key` on them is ignored (a stored request fingerprint would be a hash of the passphrase). `TestGatePassphraseIsNeverStoredInTheClear` searches every column of every table for the passphrase, a wrong guess and the next one. Request logs carry no bodies.

Wrong tries are counted per project in three buckets, two hard and one soft. **Hard (the lockout):** the person (so signing out and in does not reset it) and the session. Each earns, after three free wrong tries, a wait of 30 seconds, doubling, at most an hour, **even with the right passphrase** (429, `retry_after`); a right passphrase clears both. I read "per (person, session)" as both buckets, not as a pair: a pair would let a sign-out and sign-in reset the count. **Soft:** the client address (a keyed hash of an IPv4 address or an IPv6 /64, never the address). Everyone behind one address (a household, an office, a proxy that hides the real address) shares it, so it must never lock a person out who has done nothing: it has six more free tries than a person, and its wait applies **only to a caller who has at least one wrong try of their own**. A right passphrase takes back its own count on the address and leaves the rest. So one address that keeps guessing, with fresh sessions or other accounts, is slowed after ten wrong tries (`TestGateAddressBucketSlowsAnAttackerAndNobodyElse`), and a read-only member's wrong tries cannot lock the owner out from the same address (`TestP7ReviewReaderCannotLockTheOwnerOut`, `TestGateLockout`). The try is counted before it is checked, so guesses made at the same moment cannot slip past (`TestGateLockoutCountsBeforeChecking`). A counter quiet for 24 hours starts again. At most two hashes run at once for strangers' tries (a third gets 429 "busy" after nothing is counted); the owner's own hashing of a new passphrase has its own two slots. The general per-person request limit also applies.

### What else I decided

- **Change and clear need the current passphrase.** Otherwise the person with the session just clears it. They share the lockout. Only a member who is the project's **owner** may set or clear; an administrator acts as an owner everywhere else but must not lock a book's author out, so an administrator cannot set one.
- **A forgotten passphrase.** Three cases, each tested (`TestGateClearRules`, `TestGateClearByAnAdministratorWhoIsAMember`, `TestPassphraseClearCommand`); `TestGateOverMCP` covers keys over MCP (tools and resources):
  - An instance administrator who is **not the project's owner** (not a member, or a member with another role) clears it without the passphrase, even from a session that has not unlocked (`DELETE /api/v1/projects/{id}/passphrase`); it is recorded as `project.passphrase.admin_clear`.
  - An administrator who **is the owner** (the usual one-person instance) needs the passphrase like any owner. If the person with a borrowed laptop could clear it without, the gate would protect nothing in exactly the setup it is most wanted in, so I chose not to give the owner-administrator that shortcut.
  - The way back for that person is the operator's command on the server with the server stopped: `scriobh passphrase clear --project ID`, recorded as `project.passphrase.operator_clear`. The operator holds the text anyway (a).
  Nothing is lost by any of it: the text is not encrypted.
- **An administrator who is not a member** is gated like anyone else if they call the ops (and a stranger to the pages, as before).
- **Share links.** A link for a protected project is a separate door, with its own optional passphrase and settings (`/s/{token}/…` is not a person's session and is not touched). Managing links (`share.*`) is a project op, so it needs the project unlocked: a person who has not unlocked cannot make, change or revoke one. A link that already exists keeps serving per its own settings; revoke it first if that is not wanted.
- **`entity.merge`, `compendium.merge` and a locked project.** A project the caller is on and has not unlocked (a passphrase protects it) that overrode **both** entities of a merge refuses the real merge (409 `locked_projects`, with a count and never the project's name) and appears in the plan as `locked_override_conflicts`; in a move between Compendiums it is a `blocking` reason. Otherwise the merge would delete that project's override of the entity being merged away, unseen and unrecorded. Unlock the project and the conflict is the caller's to choose per project, as for any project they can open (`TestGateMergeRefusedWhileALockedProjectOverrodeBoth`). A project the caller is not a member of is as before: counted as hidden, the target's override kept. The before-state record never holds a project's override.
- **The Compendium is its own object.** Its membership is separate from the project's, and it may serve several projects, so the project's gate does not cover `/c/{slug}` or the Compendium ops: its entities, timeline and research are readable by its members whatever the book's passphrase. What the gate does cover is everything of the book that appears in those views (the documents an entity appears in, scenes linked to events, counts), because they check the project through the role function. Someone who wants the bible locked too should keep it in a Compendium whose members are only people who may open the book. Not built: a switch that also locks the primary Compendium.
- **Pinned keys and the pin.** Unchanged: a project pin implies the primary Compendium; the gate decides whether a pinned key may reach the project, not the Compendium.

### Data

`project_gates` (the hash, epoch, idle minutes, allow keys), `project_unlocks`, `project_unlock_failures`, `project_gate_key_use`: migration 1850, all with cascades on the project, and on the session or key. No existing table changed, so there is no upgrade fixture. In DATA-MODEL.

### Ops, routes and what a screen needs

Ops: `project.passphrase.get` (any member, locked or not; owner sees idle, allow_keys, wrong tries, key use), `project.passphrase.set`, `project.passphrase.clear`, `project.unlock`, `project.lock`. Also `Project.protected`/`locked` on `project.list`/`project.get` and `protected_projects` on `key.list`. Errors a screen handles: 403 `data.reason` `project_locked` (with `project_id`, `title`, `slug`), 403 `project_keys_refused`, 422 on `passphrase` (wrong), 429 with `retry_after` (waiting or busy), 422 `dry_run_unsupported`.

Routes added: `GET /p/{slug}/unlock` (the unlock page, `web/templates/project-unlock.html`, a plain form: it works without script), `POST /ui/projects/{id}/unlock`, `POST /ui/projects/{id}/lock` (the forms; `next` is followed only if it is a page of the same project). The settings screen (set, change, clear, idle, keys) is for the UI agent: it should post to ops through routes under `/ui/projects/{id}/passphrase/…` or `/p/{slug}/passphrase`, the two shapes `gateExemptPage` lets through while locked. Any other new page under `/p/{slug}/` or `/ui/projects/{id}/` is gated with no work; one outside those shapes fails `TestEveryRouteIsClassifiedForTheGate` until classified. Words are in `en.json` under `project.gate.` (the unlock page, the notice, the settings screen's labels and the two refusals); the settings words are provided for the screen and not used by any template yet. The unlock form's own messages go through the catalogue (`project.gate.wrong`, `project.gate.wait`); the Compendium screens' words are under `compendium.manage.`, provided for the screens and used by no template yet.

### What stays in the browser

Checked in `web/static` and the service worker. The worker keeps `/static` and the offline page and no page (`/offline` has no text), and every page is `Cache-Control: no-store`, so the browser's cache and the Back button hold no manuscript. Two things do remain, and the gate cannot reach them from the server:

- **A page already open** keeps its text on the screen after the project locks, until the person navigates. The next save or fetch is refused with 403 `project_locked`. The lock form (`POST /ui/projects/{id}/lock`) redirects to the library, so locking from the page leaves; **the screen work must answer 403 `project_locked` from a script (the editor's autosave, the tree, the inspector) by replacing the view with the unlock page**, or the idle limit protects only the next request.
- **Unsaved drafts** the editor keeps in `localStorage` (`scriobh:draft:`, `held`, `scratch`, `research`) survive a lock and an idle expiry; today only sign-out clears them (`web/static/js/shell.js`). They hold unsaved text only (a draft is dropped when the server has the text), and are keyed by person. The screen work should clear a project's drafts when it locks it, with the same confirmation sign-out gives when a draft holds text the server does not have. Not built here (backend only).

### Numbers

On an Apple M4 Max, no load: a gate check is 13 µs for a project with no passphrase and 14 µs for one that is unlocked (one indexed read, one joined read); budget 200 µs, test bound 400 µs. A request asks the gate more than once: the registry's check, the page layer's (for the REST shapes it also looks the document's project up: one more indexed read), the role function for each hand-written check, and, on a stream, once per event; a REST call to a project op costs about 30 to 40 µs of gate, which is measured as the sum and not separately. Setting a passphrase hashes once: 21 ms (budget one second). A check never writes inline.

### Not verified

No browser run: the unlock page, its redirect round trip and the htmx `HX-Redirect` are tested as HTTP, not clicked. The address bucket depends on `SCRIOBH_TRUST_PROXY` being right behind a proxy (the same as the share links). Idle times are checked with an injected clock, not by waiting. The documents `skills/`, `docs/API.md` tables and the CLI and MCP snapshots are stale until `make skill` is run at the merge (`TestCommittedSkillIsCurrent` and the MCP catalogue test say so, and nothing else).

## 2. Compendium management (F-478)

### The ops

| Op | What |
| --- | --- |
| `compendium.primary.preview`, `compendium.primary.set` | Change a project's primary Compendium with the preview's fingerprint. Project owner. |
| `compendium.merge.preview`, `compendium.merge` | Move a Compendium's entities (all, or `entity_ids`) into another. Owner of both. `compendium.merge` is `Permanent` (a name clash settled by merging two entities is `entity.merge`, which is). |
| `compendium.split.preview`, `compendium.split` | Move chosen entities into a new Compendium. Owner. `Permanent` too: with `crossing=drop` or `drop_aliases` it removes rows no op restores (they are in the before-state). |
| `compendium.change.list`, `compendium.change.get` | The record of a merge or split, with its before-state. Owner of either side. |

`project.compendium.set` stays, previews with `dry_run`, and shares its engine (`setPrimaryRun`) with `compendium.primary.set`. Every one of the eight is **session-only** (`Auth.ForSessions`): an API key of any scope or pin is refused over REST, not only kept out of the MCP catalogue (`TestRestructuringOpsAreSessionOnlyAndNoKeyCanCallThem`). The previews name hidden counts and pinned keys, the moves change what projects and keys reach, and the change record holds the text that was merged away. They are also excluded from MCP by name in `internal/mcp/catalog.go`, with reasons.

### One plan function, and the fingerprint

`entities.PlanMove` works out everything and writes nothing. The preview returns its plan with a **fingerprint**; the change recomputes the plan inside its own write transaction and refuses (409 `plan_changed`, with the new fingerprint) unless it is the one it was given, and refuses with no fingerprint at all. A change made between the two (an entity added, an alias that now clashes) shows up as a refusal, never as a surprise. That is also the concurrent-edit rule (`TestMergeFingerprintRefusesAChangedPlan`, `TestMoveWithConcurrentEdits`). The primary change does the same with its own plan.

### What a move does, row by row

An entity keeps its id when it moves, so what names it by id follows with no rewrite: its aliases, its mention rows, a project's override and link, a scene's point of view or place, the older ids merged into it. What carries a Compendium id or a same-Compendium rule has one decision each, in the plan:

| Row | Decision |
| --- | --- |
| entity name and aliases | a clash with a name or alias in the target needs the caller's choice per entity (`resolutions`): `rename`, `merge` into a named target entity of the same kind (`entity.merge`'s rules and plan, across the two: the source is deleted, its old id redirects, its name becomes an alias), `drop_aliases` (name must not clash; the dropped aliases are in the before-state), or `skip` (stays in the source). Unanswered: refused (422, `data.unresolved`) |
| relationships | both ends move: they move; one end only: refused, or dropped and kept in the before-state with `crossing=drop` |
| timeline events | an event whose people all move moves with them; one only some of whose people move stays, and the moved people are taken off (`crossing=drop`) or the move is refused; an event with no people stays; a place that stays behind is cleared on a moving event |
| field definitions | one the target lacks is copied (if a moving entity has a value for it); one the target defines with another type is left, and the plan names the key |
| search rows, redirects | follow the entity (`entity_text` is rewritten in the same transaction) |
| mention index | the stamp of every project either Compendium reaches (before and after) is bumped in the same transaction, so a worker sees the move and the stamp or neither, and rescans |
| scene links of moving events | `timeline_event_docs` and `timeline_narrative` rows go with their event; the plan counts them (`event_scene_links_moving`): the target's timeline then lists scenes of books that may not use the target, shown only to whoever can open that book (a locked book is not shown) |
| scenes, overrides, links | unchanged (by id); the plan counts them; a project that used the source and not the target stops seeing the moved entities (overrides dormant, kept) |
| projects | counted and, where the caller can open the project (so a locked one is only counted), named with what leaves; `follow_projects` adds a read-only reference to the target to those the caller owns and that are not in the trash |
| API keys pinned to the source | re-pointing widens a key to the target's other material, so the default is **refuse** (clear 409 naming the count); `repoint` moves only keys whose owner is on the target; `leave` keeps them pinned to the source. Only the caller's keys are named; others are counted |
| research, tags, attachments, scratchpads, members, references, trashed entities | not moved: they stay with the source, and the plan says how many |
| the source Compendium | not deleted: its owner decides |

The target and the source are both checked in the transaction (owner role, not in the trash: the registry sees only the Compendium in the path). A Compendium in the trash is refused either side (409 `compendium_trashed`); a project in the trash is refused for `compendium.primary.set` (409 `project_trashed`, by the registry) and skipped by `follow_projects`.

**"Events and revisions kept"** I took to mean: timeline events are kept (they move with their people or stay), the durable event log is untouched (a move adds `compendium.merged` or `compendium.split` events for both Compendiums, with ids and counts, so the mirror and the live view follow), and an entity's revision number goes on (+1), which also makes a stale edit made against the old revision a 409.

**The known leftovers of the Phase 6 trashed-project notes are not widened.** A move rewrites no row of a trashed project's: overrides, links and mention rows follow by id and are not rewritten. The only write that reaches one is the mention stamp, which is derived data the index workers refresh (as the existing triggers already do), and `follow_projects` skips it.

### Undo

Each change writes one row to `compendium_changes` (1851) in the same transaction: the plan's counts and the **before-state**: every entity that moved (name and revision as they were), every entity merged away with its whole sheet and the merge plan, every relationship, event person, event place and alias that was dropped, the field definitions copied, keys repointed and references added. It is returned in the response and readable with `compendium.change.get` by the owner of either Compendium; it goes with either Compendium when that is purged. **It is a record, not an undo op:** because ids are kept, putting a plain move back is mechanical from the record, but there is no op that does it, and a merged entity cannot be re-created by one. Not built, and said so.

### Numbers and tests

A 2,000-entity move (relationships between neighbours, an alias on every fifth, custom fields): preview 96 ms, apply 469 ms, both in a test with a three-second bound; one move takes at most 5,000 entities. The entity tests are in `internal/domain/entities/move_test.go` and `move_perf_test.go`: identity and everything that follows, the invariants (no relationship, event person, event place or search row names the wrong Compendium; no two live entities share a name) checked after every test, per-entity name clashes and each answer, a merge by entity merge, the fingerprint, **failure halfway** (a trigger fails the move on its last entity and, for a split, at the very last step: a full dump of the touched tables is equal afterwards, and no Compendium was left behind), partial moves with and without `crossing=drop`, key pins (refuse, leave, repoint, a key of someone not on the target), access (stranger, editor, owner of the source who is only an editor of the target, a key, the administrator, either Compendium in the trash, a project in the trash), `follow_projects`, concurrent edits during a move, and the primary change (the plan, a stale fingerprint, the mention stamp, the older op still working).

### Not built, not verified

Not built: an undo op; the screens (preview, the per-entity choice, the record); moving research notes, tags, attachments, members and references; moving trashed entities; deleting the source; a `follow_projects` that switches a project's primary (use `compendium.primary.set`, which the project's owner decides). Not verified: the screens, the 5,000-entity cap only as a refusal (no 5,000-entity move was timed), the CLI's name resolution of the new ops (the generated CLI snapshot is stale until `make skill`), and a merge of Compendiums larger than the 5,000-entity cap (refused, with the message to move them in parts).

## 3. Fix round after the independent review

The review's nine findings, in the order they were fixed. Tests named are in `internal/server` unless said.

- **R1, the old passphrase could unlock after a rotation (critical).** The hash was read, checked (about 20 ms of argon2) and the unlock written in a later transaction that read whatever epoch was current. `unlock`, `set` and `clear` now keep what they checked, `(hash, epoch)`, and re-read both **inside the writing transaction**; anything else is 409 `passphrase_changed` ("The passphrase changed while you were typing it. Try again."), also shown by the unlock form. The epoch alone would not do (a clear followed by a new passphrase comes back to epoch 1 with another hash), so the hash is compared too. `Service.SetAfterVerifyHook` is a one-shot hook between the check and the write, for tests only; `TestGateWriteNoticesAPassphraseThatChangedAfterTheCheck` uses it to rotate, to clear and set again, to race a stale `current` on `set` and on `clear`, deterministically. The review's timing sweep stays as well.
- **R2, `allow_keys` let in every member's earlier key.** Changed approach: acceptance is per key (see (c) above, migration 1852).
- **R3, the route test failed open.** Rebuilt as an exact-pattern table with call-site accounting (see the sweeps above); the address gate now also covers `/api/v1/projects/{id}` and `/api/v1/docs/{id}`.
- **R4, a locked session's stream went on delivering until the heartbeat.** The gate is asked for every project event (14 µs), and the gate signals the streams watching a project when it locks (`Service.Watch`); the stream asks again and ends with `bye` at once.
- **R5, the address bucket could lock the owner out.** Person and session are the hard lockout; the address only slows, and only a caller with a wrong try of their own (see (e)).
- **R6, a preview failed open.** `CallDry` built the environment with no services, so the locked-project filter found no gate. The gate service is now registered as a *preview service* (`Env.SetPreviewService`: a named, read-only decider that a dry run's environment has too; nothing else is copied), and `LockedProjects`, `markProtected` and `CheckMint` return an error, rather than "none", when an environment has a gate but no service. `project.lock` is `NoDryRun` (it changes what the process remembers). `TestGateDryRunMarkReadIsNotAnOracle`, `TestP7ReviewDryRunAgreesWithTheRealCallWhileLocked`.
- **R7, the `share_test.go` flake.** `LIKE '%Sam%'` is case-insensitive and the audit rows hold ids; the test compares with `GLOB` (case-sensitive). Not a product change.
- **R8, a merge deleted a locked project's override unseen.** Changed approach: the merge is refused while a project the caller is on and has not unlocked overrode both entities (see the decision list above). The alternative (keep the override in the record) would have put a project's private text in a record its Compendium's owner reads.
- **R9, the change record and the restructuring ops were reachable with keys.** All eight are session-only.

What this round did not do: a gate that also locks the primary Compendium, and the screens, as before. `project.passphrase.get` now lists, for the owner, their own live keys and the keys that have tried the project, each with `approved`; the settings screen needs it for the approve and withdraw controls.
