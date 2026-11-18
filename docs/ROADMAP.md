# Roadmap

Scope-based, not date-based. Each phase ends with a release that a real person could use for a week and report on. Estimates assume one developer working with AI assistance, part time; treat the weeks as relative weight, not promises.

The rule that keeps this honest: **each phase must be usable on its own.** No phase is a pile of half-features waiting for the next one.

```
Phase 0  Foundations        repo, CI, design tokens, auth shell
Phase 1  v0.1 First Draft   write, save, structure, search, export
Phase 2  v0.2 Structure     outline, revisions, mirror, Git push, goals
Phase 3  v0.3 The Compendium  entities, timeline, research, CLI, platform
Phase 3.5 v0.3.5 Alternatives  manual variants, ghosting, stash
Phase 4  v0.4 Writing Partner  AI, Lab, AI alternatives
Phase 5  v0.5 Revision      comments, suggestions, sharing, real exports
Phase 6  v1.0 Publish       compile presets, print, static site, MCP, hardening
Phase 7  v1.1 Second Wave   EPUB import, external manuscripts, long tail
Phase 7.5 v1.2 Writing Studio  goals, streaks, analytics, momentum
Phase 8  v2.0 Desktop       Tauri shell, embedded OIDC (built, unsigned; the updater ships in v2.3.0 and has not run)
Phase 9  v2.2.0 Covers      your own cover on screens and in ebooks; the print cover ships in v2.3.0
Phase 10 v2.3.0 Updates     desktop auto-update from GitHub, after you say yes
```

---

## Priority

**The editorial core is first-class; AI is second-class.** The order below is deliberate: the Compendium (entities, timeline, research) and Alternatives ship before the AI partner. If time is short, the AI phase slips; the editorial phases do not. No AI feature may become a dependency of drafting, outlining, or continuity.

---

## Phase 0 — Foundations

**Goal:** a repository that builds, deploys, and refuses to let the design drift.

- Go module, `cmd/scriobh`, `internal/` layout, `Makefile`, `Dockerfile`, `compose.yaml`.
- Embedded migrations with a runner and a pre-flight backup.
- OIDC login against the **stub identity provider** (same discovery/PKCE path as production), session cookie, logout, group → role mapping, auth middleware. Passage is the production IdP.
- `internal/ops` registry skeleton with one working op and one UI route proving the pattern.
- Server-rendered layout: header, tree, page, margin, toast, dialog, command palette stub.
- Design tokens for Paper: colour, type scale, spacing, radii, motion; Paper and Ink themes; contrast script.
- Editor spike: ProseMirror schema, Markdown parse/serialize, mobile IME and paste checks; the round-trip test exists before the editor does.
- `/healthz`, structured logging, CSP, CSRF, security headers, request limits.
- Desktop-ready smoke: start on `127.0.0.1:0`, read the printed port, and drive the app with no reverse proxy and no external IdP.
- CI: `go vet`, `go test`, template lint, contrast check, build a container image.

**Exit criteria:** `docker compose up` → sign in through the stub IdP → see an empty library → create a project → see it after a restart; the same client config is documented for Passage. CI is green, the image is non-root, and the app also runs on an ephemeral loopback port with no proxy.

---

## Phase 1 — v0.1 "First Draft"

**Goal:** a writer can finish a short piece in Scríobh and trust it.

- Library: project tiles, new-project wizard, project kinds, trash and archive.
- Manuscript: tree (parts/chapters/scenes), reorder, prev/next, open tabs, counts.
- Editor: rich text (ProseMirror) with a Markdown Source toggle, toolbar, input rules, autosave, conflict handling.
- Command palette and quick switcher; FTS5 search with snippets.
- Markdown export, single file and bundle.
- Themes: Paper and Ink, system following; editor preferences.
- Mobile layout for the editor and the tree.
- Keyboard access, focus rings, reduced motion.

**Exit criteria:** 10,000 words written across 12 chapters on desktop and phone; every save survives a hard reload and a server restart; the Markdown round-trip test passes on every construct the editor can produce; export opens correctly in another Markdown editor; the editor is interactive in under 250 ms on a mid-range phone and typing stays under 16 ms per keystroke; a 20,000-word chapter remains smooth, and the long-document guardrails are in place.

**Deliberately absent:** AI, comments, EPUB, goals, entities, outline view, alternatives and the Lab.

---

## Phase 2 — v0.2 "Structure"

**Goal:** the app holds a book-shaped project, not just a pile of documents.

- Outline view with synopses, targets, status, drag reorder.
- Front/back matter, chapter numbering, scene metadata (POV, location, story date).
- Wiki links, auto-create, backlinks.
- Revisions: automatic snapshots, named versions, restore.
- Continuous Markdown mirror with `project.json`; backup and restore commands, with scheduled local backups (pulled forward from Phase 5).
- Push-only Git sync: Forgejo and GitHub presets, SSH deploy keys first, debounced push, status, and force-push as an explicit UI action with a bundle backup.
- Goals v1: project target, daily target, streak.
- Import a folder of Markdown.
- Focus mode, typewriter scrolling, sticky mobile formatting bar.
- Theme set: Slate, Sepia, Moss; accessibility pass; contrast in CI.

**Exit criteria:** a 60,000-word project is navigable, searchable, and versioned; `data/mirror/` contains a complete, readable Markdown copy; restoring yesterday's chapter takes three clicks; the outline can reorder a part without losing a word; a project pushes a clean, readable history to a remote, and a diverged remote is detected and never overwritten silently.

---

## Phase 3 — v0.3 "The Compendium"

**Goal:** the book gets a Compendium around it, and the platform grows up around the book. This is the first-class editorial phase.

- A **Compendium** as a first-class library item: entities, research, clippings, timeline, and glossary. It can exist with or without a project, and a project has at most one primary Compendium.
- Compendium membership separate from project membership, so a beta reader on one book cannot read the whole bible. A Compendium can be created and worked on with no project open.
- Adding a project editor prompts to add them to the primary Compendium (default yes); reviewers and readers default no. No silent grants.
- Entity bible: characters, places, items, organisations, concepts; custom fields, aliases, relationships, merge with a dry run.
- Mention detection, the mention index, first appearance per project, and the "in this scene" sidebar.
- Project-scoped overrides of shared entities, read-only references from other Compendiums, and whole-Compendium references.
- Timeline: story dates, narrative order, participants; one Compendium timeline with a per-book order.
- Research notes, clippings, attachments, the research board, and the scratchpad.
- Outline sorting by numerical order, timeline, or POV, with toggleable POV, story-date, status, and word columns.
- Corkboard and status board; reading mode.
- Platform: REST API and CLI, op schema, PWA shell, SSE and single-writer lease, Git repository import, HTML export and cover generator.
- An agent-facing CLI: name-based addressing, JSON output, `--dry-run`, stable exit codes, idempotency, and scoped keys restricted to one compendium or project.
- A generated agent skill: `scriobh --skill` for the full skill, `--skill compendium` for one recipe, install into coding agents, and `skill check` against the live schema. `make skill` regenerates it and CI fails when it is stale.
- `scriobh bench` and the 500,000-word performance gate.

**Exit criteria:** two projects share one entity bible without copying it; a Compendium can be created, edited, and exported with no project open; a project can override an entity for its own book without changing the Compendium record; adding a character and finding every mention takes under a minute; a timeline contradiction is visible with AI switched off; a 500,000-word project opens a chapter in under 150 ms and searches in under 80 ms; a script can create a project and a chapter through the API alone; a repository clones into a mapped project and the preview is accurate before anything is written; an agent with a compendium-scoped key creates a compendium, adds twenty entities, and drafts a chapter through the CLI, and `scriobh skill check` is green against that server.

---

## Phase 3.5 — v0.3.5 "Alternatives"

**Goal:** revision as play, not as a decision. This phase is deliberately not AI.

- Alternatives at word, sentence, and paragraph scope, with a side panel and in-place cycling.
- Manual alternatives, user/AI markers (AI variants arrive in Phase 4), and an always-available original.
- Wavy underline, end dots, the middle-dot indicator, automatic a/an, optional audio, and an undoable delete animation.
- Ghosting and revive for selections. Ghosted words are excluded from the count but remain in the document.
- The stash: an overflow panel for unused paragraphs, notes, and outlines, searchable and excluded from exports.
- A documented Markdown representation for alternatives that survives export and re-import.
- Cycling records a revision; the chosen text is canonical for search, word count, AI context, and export.
- Keyboard-first throughout; reduced motion and muted devices respected.

**Exit criteria:** a writer can keep three versions of a word, a sentence, and a paragraph and cycle them without leaving the page; the original is always one action away; an export uses the chosen variants and re-importing it restores them; ghosted text can be revived, is not counted, and is exported by default; a stashed paragraph can be dragged back; every one of these works with AI disabled.

---

## Phase 4 — v0.4 "Writing Partner"

**Goal:** AI that a suspicious writer will actually use, added to an editorial core that already stands on its own.

- Provider adapters: OpenRouter, OpenCode API, OpenAI-compatible, Ollama.
- Encrypted credentials; per-project AI policy; local-only enforcement.
- Context builder with chips, token estimates, and a preview of the exact payload.
- Selection actions, diff review, chat drawer, recipes, style guide.
- AI alternatives for a selection, and the Lab: mark convoluted sentences, flag tone mismatches, fix punctuation, mask long sentences and hedges, and four trim modes. The Lab dims or marks; it never rewrites.
- Critique mode that returns notes, not prose; rolling chapter summaries.
- Accept all suggestions from one review in a single action, with a preview and one revision.
- Continuity audit against the Compendium bible and timeline; entity extraction into suggested sheets; redaction mode; per-action model defaults and fallback chains.
- Cost caps, usage log, audit log.
- A red-team pass on prompt injection, credential leakage, and cost exhaustion.

**Exit criteria:** the same rewrite can be run against OpenRouter, a LAN Ollama model, and OpenCode, with identical UX; the context preview matches what the provider receives byte for byte; a monthly cap stops a runaway loop; the Lab marks at least 20 suggestions in a chapter and every one is accepted or rejected individually; with every provider switched off, Phases 1 through 3.5 remain fully usable.

---

## Phase 5 — v0.5 "Revision"

**Goal:** hand the manuscript to another human and get it back improved.

- Comments with anchors, threads, resolve, mentions, review queue.
- Suggestion mode with accept/reject.
- Project members and roles; read-only and comment-only share links.
- DOCX with US Standard Manuscript (Shunn) and UK manuscript presets: Times New Roman or Courier New, double-spaced, title page, running header, chapter page breaks, `#` scene breaks, real Word styles. Styled DOCX and EPUB 3 as well.
- Compile preview before export; front/back matter; optional submission bundle.
- Import DOCX and Obsidian.
- Admin console; quotas; diagnostics bundle.
- i18n scaffolding, RTL manuscript support.

**Exit criteria:** an editor can read the whole book in the browser and leave 40 anchored comments; the writer resolves them in one pass; the EPUB validates and opens on a Kobo; the US manuscript DOCX opens in Word with the correct title page, running header, double spacing, and scene breaks; a beta reader with a link and no account can read without seeing settings.

---

## Phase 6 — v1.0 "Publish"

**Goal:** finish the thing and ship it.

- Compile presets; print-ready PDF with paged HTML; chapter ornaments; covers.
- Static site publishing for blog mode (HTML + RSS to a folder).
- MCP server, agent skill, webhooks.
- Import from Scrivener and WordPress/Ghost (built as a backend: see FEATURES § AK).
- Full documentation: install, configure OIDC, backup/restore, upgrade, API, AI providers. **Written**: [OPERATIONS.md](OPERATIONS.md) (quick start, OIDC registration, proxy, backup and restore, upgrades, AI providers quick path) and [API.md](API.md) (the REST reference, its op table generated from the registry and checked by CI); the exit criterion below, that two independent people complete the install guide unaided, has not been tried, and the container image path has not been run.
- Hardening: authz sweep test, CSP smoke test in Chromium, backup/restore rehearsal, upgrade from v0.1 data.
- Accessibility audit; screen-reader pass; contrast and zoom checks.
- Performance and long-run soak test with a 150,000-word project.

**Exit criteria:** a real book goes from blank project to a printed proof and a validated EPUB without leaving the app; a restore from backup is performed on a clean machine and verified; two independent users complete the install guide without asking a question.

---

## Phase 7 — v1.1 "Second Wave"

**Goal:** bring books that already exist into Scríobh, and finish the long tail.

- EPUB 2/3 import: spine order, metadata, cover, images, footnotes, with a mapping preview.
- External provenance: the source file is kept intact, hashed, and recorded; the project knows which edition it came from.
- Revise an imported book with a diff against the source edition; compare two editions chapter by chapter.
- Import a PDF with a text layer, with an honest fidelity report; import a Word manuscript with tracked changes as suggestions.
- Round-trip: an exported EPUB re-imports without structural loss.
- Publish a project to GitHub or Forgejo: create or select a repository, choose visibility, push the mirror; optionally publish a compiled edition as a release asset.
- A project view passphrase: an application-level gate for a project or share link, not encryption.
- Long tail carried from the original plan: semantic search with a local embedding model, the optional Pandoc bridge, Kindle delivery, email digest, document-level presence, custom typography, Scrivener import, multiple Git remotes, commit signing, CI triggers.

**Exit criteria:** an existing EPUB becomes an editable project with correct chapter order, images, and footnotes; a second edition can be diffed against the first and the differences are reviewable; an export re-imports without structural loss; importing a 300-page book with images takes under a minute on the target hardware; a project can be published to a repository in one reviewable flow, and a view passphrase can be set and removed.

---

## Phase 7.5 — v1.2 "Writing Studio"

**Goal:** the writing habit, measured without nagging. The second half of the Phase 7 wave.

- Daily, weekly, and monthly goals; project goals and custom goals; goal history. Goals count **words added**, with net words shown separately.
- A project goal dashboard with **2 active / 1 completed** style counts, per-goal progress, today's target, and a quick "write N words today" action.
- A habit-streak card: current streak, longest, today's words against target, and a target-hit message. A day counts only when the goal was met.
- A 24-week activity calendar with Mon/Wed/Fri rows, month labels, five intensity steps, a writing-days count, and an all-time view. No red "missed" days.
- Four period cards (today, 7 days, 30 days, 365 days) with a delta against the previous period and a sparkline.
- A words-written line chart with 30-day and all-time ranges; pace and most productive days and times.
- A pace forecast to the deadline or word goal.
- Sprint and session statistics.
- Milestones and gentle nudges: "450 words to go today", "50% of today's goal", streak encouragement.
- A gentle productivity trend comparing the last three weeks with the previous three, with resources rather than a warning.
- A private progress card (PNG or private link); no social posting.
- Optional weekly or monthly progress email over SMTP.
- CSV and Markdown export; every number computed locally.

**Exit criteria:** the dashboard matches the reference: four period cards with deltas and sparklines, a habit-streak card, a 24-week calendar with Mon/Wed/Fri rows, a project-goals panel with active/completed counts, and a words-written chart; a writer can set daily, weekly, and monthly goals and see a pace forecast; every number reconciles with a hand count; goal progress counts words added and streaks count goal days; no analytics data leaves the instance; the progress email is opt-in and off by default; no screen shames a missed day.

---

## Phase 8 — v2.0 "Desktop"

**Goal:** the same application, double-clickable, with no server and no browser tab.

- A Tauri 2 shell around the unchanged Go binary (Electron fallback if a target webview cannot carry the editor).
- An embedded OIDC issuer on loopback with a single local profile, so desktop still signs in through the same OIDC code path.
- Ephemeral port, single instance, graceful shutdown, data directory in the OS location, secrets in the OS keychain.
- Native menus, open/save dialogs, reveal in folder, drag-and-drop, deep links, and file associations.
- Signed builds and auto-update: Linux and macOS first, then Windows before v2.0 exits. Auto-update is released in v2.3.0 (phases U1 and U2 of [UPDATES.md](UPDATES.md): by consent, from GitHub, installed only on Restart to Update) and has never run against a real release; code signing (a Developer ID, a Windows certificate) is not built.
- Fully offline operation, including a local model; native print and export dialogs.
- A switch back to an external IdP for a shared or team install.

**Status:** built and run unsigned on one Mac; the exit criteria below are **not met** (nothing is signed or notarised, no installer has been used on a clean machine, Windows has never run, and nothing has ever updated in place: the updater ships in v2.3.0 and has run against no real release). See [DESKTOP.md](DESKTOP.md) and the Phase 8 release notes in [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md).

**Exit criteria:** a non-technical user installs a `.dmg`, `.msi`, or AppImage on a clean machine, creates a local profile without a server or a password, writes and exports offline, and updates in place; the browser deployment passes its full test suite unchanged; the wrapper introduces no second authentication path and no privileged filesystem access from the web layer. Linux and macOS ship first; Windows is required before v2.0 is called done.

## Phase 9 — v2.2.0 "Covers" (C1 released in v2.2.0; C2, print, released in v2.3.0; a printer's previewer is still to do)

**Goal:** the writer's own cover, front and back or one wrap, wherever a cover appears. Design: [COVERS.md](COVERS.md).

- C1, the front: cover layouts (sides or one wrap), cleaned uploads stored as attachments, the cover page and the tree's Cover row, and the layout's front in the library, `project.cover`, the EPUB, the HTML export, the site, the mirror and Git.
- C2, print: `export.cover` from a layout with printer profiles, a proof page and the checks; the optional EPUB back cover and interior cover page. Released in v2.3.0: the back end, and the Compile page's cover section (the profile, the drawing from the layout's pictures, every finding in words, three downloads: the file for the printer, the front alone and a proof).

**Exit criteria:** as listed under "Phases" in [COVERS.md](COVERS.md), and what was not verified is stated: no print-on-demand upload or printed proof of a produced cover. v2.3.0 is released with print covers, and that is still the open item.

## Phase 10 — "Updates" (U1 and U2 released in v2.3.0; the rest is later)

**Goal:** the desktop app updates itself from the project's GitHub releases, when its owner has said yes to one question, and never interrupts writing. Design: [UPDATES.md](UPDATES.md).

- U0, decisions: the owner's decisions and the update key (the owner makes it), the macOS keychain spike, a staging repository.
- U1, the pipeline: the version stamped from the tag, signed update packages, `latest.json` made by our own script and published last, on a staging repository first.
- U2, the shell: the first-run question, a daily check, a background download, Restart to Update, macOS and Windows and the AppImage in place, a notice for the `.deb`; tested from one release to the next on all three systems.
- U3, signing: macOS Developer ID and notarisation (so the keychain item survives an update), and Windows code signing. U4, the bridge release: the first release that carries the key, installed by hand once. U5 and U6 are optional: an in-page notice, and a signed apt repository for the `.deb`.

**Status:** v2.3.0 is released with print covers and desktop auto-update. The updater has run against no real release yet, and v2.3.0 is the first real run; people on v2.2.x or earlier install it by hand, once. The desktop builds are unsigned.

**Exit criteria:** as listed under "Phases" in [UPDATES.md](UPDATES.md): a clean machine of each system updates from one release to the next through the real menu, the refusals refuse, and what was not seen (Windows, the macOS keychain without a Developer ID, anything on Intel) is said plainly.


---

## Beyond v2.0

Ordered by likely value, not promised:

1. Suggestion and comment UX refinements; reviewer progress.
2. Document-level live co-editing (not remote cursors).
3. A mobile shell that reuses the same server, if the PWA is ever not enough.
4. Plugin-free integration recipes for static site generators and newsletter tools.

---

## What gets cut first

If a phase runs long, ship the smaller thing:

| Cut | Keep |
| --- | --- |
| Corkboard and board views | Outline view with synopses |
| Ghost-text continuation | Selection actions and chat |
| Timeline view | Story-date field on scenes |
| EPUB and print PDF | DOCX and HTML |
| Suggestions | Comments and versions |
| Static site publishing | HTML export folder |
| Webhooks | REST API and CLI |
| The whole AI phase | Every editorial phase, unchanged |
| Progress emails | In-app analytics |
| Advanced pace and time-of-day charts | Basic counts and the 24-week calendar |
| AI alternatives and the Lab | Manual alternatives and the Compendium tools |
| Semantic search | FTS5 search |
| Presence | Activity feed |
| Git push sync | Manual export to a folder, then `git add` |
| EPUB import | Markdown and DOCX import |
| Desktop wrapper | The web app, installed as a PWA |
| Audio cues and the delete explosion | Keep the markers and the cycling |
| AI alternatives | Manual alternatives |
| Stash drag-back | Copy and paste from the stash |
| Lab trim modes | Mark and mask only |

---

## Definition of "pleasant" per release

A phase is not done until all of these hold for the features it adds:

- It works at 390 px with one thumb.
- It works with the keyboard alone.
- It works with the network cut, or says clearly why it cannot.
- It has an empty state that teaches the next step.
- It never loses data on a reload, a crash, or a second tab.
- Its destructive actions are reversible.
- Both themes are legible and contrast-checked.
- It adds no outbound request the user did not configure.
- It runs on `127.0.0.1` with no reverse proxy and no external IdP (desktop smoke).
- Alternatives never make the prose unreadable, and the original is one action away.
- The JS budget still holds (`docs/ARCHITECTURE.md`).
- A new contributor can find its op, its handler, its template, and its test.

---

## Risks and their mitigations

| Risk | Mitigation |
| --- | --- |
| Scope creep into a notes app | The non-features list is part of the spec; every addition must displace something |
| Editor churn (rich surface ↔ Source view) | Markdown is the storage contract and the round-trip test is the guard; the surface can be replaced without touching data |
| AI cost surprises | Per-run estimate, hard caps, local-first defaults, usage log |
| Continuity features becoming a second product | Entity sheets and deterministic mentions ship before any AI analysis |
| Export fidelity eating months | Start with Markdown/HTML, add DOCX/EPUB with well-scoped libraries, make Pandoc optional rather than mandatory |
| SQLite concurrency under a household | WAL, one writer, short transactions, single replica, documented limit |
| OIDC setup friction | A tested recipe for Authentik, Keycloak, Pocket ID, and Authelia; a "test connection" button; clear errors |
| Mobile editing quality | Design the phone layout in Phase 1, not Phase 5; test on a real device every phase |
| Git remote divergence (someone else pushed, or the remote was rewritten) | Never pull; offer force-push as an explicit UI action with a local bundle, or a new import. No merge path exists |
| Git credentials leak into logs or exports | Encrypt at rest, redact in every log and error, and run a pre-push secret scan |
| The desktop wrapper drifts into a second product | The wrapper only launches and frames the same binary; all features stay in the web app; Phase 0 constraints are tested from Phase 1 |
| SQLite cannot keep up with a very large manuscript | `scriobh bench` at 1M words gates releases; single-document guardrails; a build-tagged CGO driver is the first escape hatch, an external index the second. Postgres only if multi-writer hosting ever becomes real |
| Embedded OIDC becomes a bypass | The embedded issuer speaks real OIDC with PKCE; no header, cookie, or "desktop mode" shortcut in the auth middleware |
