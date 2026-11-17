# Overview

The long-form description of Scríobh: what exists, what is not built, what has not been verified, the commitments behind the design, the hard constraints, what Scríobh is not, and how the pieces fit. This page was the project README until the README became an install-first page; its text is kept here, unchanged apart from relative links. The README is [../README.md](../README.md).

---

**A quiet room for the long thing you are writing.** Self-hosted, private, and built for the whole life of a piece of work — a blog post, an essay, a report, a novel.

Scríobh (Irish, *write*) is a web application for long-form writing that you run on your own machine or home lab. One Go binary, one SQLite file, your words on your disk. Sign in with the identity provider you already have. Bring your own AI keys, including a model running on the same box. Keep a plain-text copy in a Git repository you control.

It is also a place to play with wording: keep alternatives for a word, a sentence, or a paragraph, and cycle them in place until the line sounds right. Write on a rich page that always keeps its Markdown underneath.

No account on someone else's server. No telemetry. No editor that phones home mid-sentence.

---

## Status

**Phase 8 (v2.0 "Desktop") is built, on top of Phase 7.5 (v1.2 "Writing Studio"), Phase 7 (v1.1 "Second Wave") and v1.0 "Publish". It is built and run unsigned on one Mac; it is not signed, notarised, released or seen on Windows, and on Linux only by the CI runner. Read "Not verified" below before relying on the desktop app.** The plan in the docs below is still the contract.

What exists and runs:

- **The desktop app (v2.0).** The same server binary in a window: a Tauri 2 shell (`desktop/`) starts it on a loopback port of its choosing with no server for you to run and no identity provider to set up. A first-run local profile (a name, no password) signs in through an embedded OpenID Connect issuer that is a real issuer on the same code path as any other provider, never a bypass; the data folder is in the system's place; the instance key is in the system keychain; the window loads one origin and no page can reach the shell; native menus, the save dialog, `scriobh://` links and `.md` and `.epub` associations; one copy at a time and a clean stop; with AI off nothing leaves the computer (a whole session is run with every outbound connection observed); an administrator can switch the instance to an external provider and back. The updater is built and never runs unless a person asks. See [docs/DESKTOP.md](DESKTOP.md).
- **The Writing Studio.** A dashboard (`/studio`) of your own numbers, computed on your server and nowhere else: daily, weekly, monthly and total goals in words added, sessions, minutes or chapters with their history, a streak that counts goal days, four period cards, a 24-week activity calendar, a words-written chart with pace and best days and hours, a pace estimate for a deadline or a word goal, sprints and a session timer in the writing view, gentle in-app nudges, compare periods, CSV and Markdown export, a private progress card as a link (no social posting), and an optional weekly or monthly progress email through the relay an administrator configures. Nothing in it is called missed or failed.
- **Writing and structure.** A server-rendered writing surface over a ProseMirror editor that always keeps its Markdown underneath, an outline with synopses and targets, wiki links and backlinks, versions you can compare and restore, goals counted in words added, search, light and dark themes with a choice of accent, and an installable app shell with an offline page. Alternatives, ghosting, the stash and the Lab keep other wordings beside the chosen one.
- **The Compendium.** Entities, aliases and relations, a timeline with continuity checks, research notes and boards, shared across projects with their own membership.
- **Review and sharing.** Comments anchored to the text, suggestions an editor accepts or rejects, share links for guests (read-only or comment), notifications, and an administrator console.
- **Output.** HTML export, compile presets (US and UK manuscript DOCX, styled DOCX, EPUB 3, Markdown), print-ready PDF with covers, a submission bundle, and static site publishing (HTML, RSS and Atom to a folder).
- **Going in and out.** Import from a folder, a Git repository, Obsidian, Word (tracked changes become suggestions), Scrivener, WordPress, Ghost, an EPUB or a PDF with a text layer (the source file is kept as an edition, and a revised book is compared against it, or two editions against each other), and, if an administrator switches the Pandoc bridge on, more formats through Pandoc; a continuous Markdown mirror; push-only Git sync with deploy keys, several remotes per project, public or private mirrors, commit signing, a trigger after a push, publishing to a GitHub or Forgejo repository and release assets; backup and restore, which brings everything that sends back switched off.
- **Reading and sending, optional.** Kindle delivery and an email digest through a relay an administrator configures (off until then); search by meaning with a local or configured embedding model (off per project until its owner previews and switches it on); a passphrase that locks a project's screens (a lock, not encryption); who else has the project open (presence, off until each person turns it on); typography for the writing and reading views.
- **AI, optional.** Providers you configure (a local Ollama or any OpenAI-compatible server, OpenRouter, OpenCode), a per-project policy, a preview of the exact request before anything is sent, recipes, chat and reviews, budgets. Nothing is written into a chapter without an explicit accept; with AI off nothing else changes.
- **Agents and integrations.** One op registry behind the pages, a REST API, a scoped CLI (`scriobh-cli`) with a generated skill, an MCP endpoint, and signed webhooks. See [docs/API.md](API.md) for the REST reference, whose operation table is generated from the registry.
- **Operations.** One binary and one data directory, `scriobh bench` and a soak run as performance gates, and rehearsals for restore and for upgrades from every earlier release.

**Not built:** a signed, notarised or released desktop app (nothing is signed and there is no update server), anything for Windows that has been run, a native open dialog (the page's own file input is the system's), reading or uploading a file from the shell (ruled out by design), and moving an existing key file into the keychain. The roadmap's exit criteria for v2.0 (a non-technical person installs a signed `.dmg`, `.msi` or AppImage on a clean machine and updates in place; Windows) are **not met**; the `2.2.0` the desktop bundle carries (`desktop/src-tauri/tauri.conf.json`, `Cargo.toml`, `package.json`) is the version of the code, not a claim that they are.

**Not verified.** Said plainly, because the docs lean on these: for Phase 8, the desktop app has been run unsigned in development builds on one Mac (Apple silicon) and nowhere else by a person: no signed or notarised build, no `.dmg`, no installer used on a clean machine, and no update ever fetched; Windows was never built into an app or run (the Go side compiles for it; its single-instance lock and the stop on standard input were never run); on Linux the CI runner built the `.deb`, installed it with apt, launched it headless under Xvfb and saw the bundled server answer, but no one has seen a Linux window, and the `Depends` line was not tried on a machine without the build's `-dev` packages; the real system keychain was exercised only by development runs on the Mac under a throwaway service name (the entry the shipped app makes, a macOS prompt, and what the app does when the keychain cannot be read were never seen in a window; that last is covered by tests and by reading), and on no other platform; no real external identity provider, no real Ollama, and no Safari-family or WebView2 behaviour beyond what the shell's own smoke saw; a server backup restored into a desktop instance was tried with test databases only; and nothing needing eyes or hands was seen (the look of the window, the menus and dialogs; no screenshot of the app could be taken where it was built) (see `docs/DESKTOP.md`, `docs/p8-notes/` and the Phase 8 notes in `docs/OPEN-QUESTIONS.md`); for Phase 7.5, no screen reader, real phone or browser other than Chromium was used on the studio's screens, no progress email was delivered by a real relay, and a real fifteen-minute sprint, a sprint across midnight and a laptop that sleeps during one were not run (see `docs/p7-notes/studio.md`, `docs/p7-notes/ui-studio.md` and the Phase 7.5 notes in `docs/OPEN-QUESTIONS.md`); for Phase 7, no real Pandoc, SMTP relay, embedding model or Git host (GitHub, Forgejo) was used (stand-ins on loopback and a development Git server were), and the quality of semantic search with a real model is unknown (see `docs/p7-notes/`); and, from v1.0: CI builds the production container image with buildah and publishes it, and its image job checks that the image's user is 65532:65532, that its production binary refuses the development issuer and that `/data` is owned by that user, but the server in the image has never been run to serve there, and the compose files are untested (they parse; `docker compose config` was run); no real identity provider (Passage, Authentik, Keycloak and the others in the guide) has been registered against this build, only the built-in development issuer; no real AI provider, MCP client, feed reader, printer, copy of Word, or physical phone has been used; the proxy snippets were not run; and the v1.0 exit criteria that need people (two independent users completing the install guide unaided, a printed proof, a clean-machine restore by someone other than the author) have not been met. [docs/OPERATIONS.md](OPERATIONS.md) and [docs/OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) list what was and was not checked, per feature.

**Try it.** `make run` builds a development binary with a built-in sign-in for three test users and prints `LISTEN http://127.0.0.1:<port>`; its data is in `./data`. To install it for real, follow the Quick start in [docs/OPERATIONS.md](OPERATIONS.md#quick-start). The performance gate is `make bench` (see [Operations](OPERATIONS.md#performance-scriobh-bench)).

| Doc | What it answers |
| --- | --- |
| [docs/VISION.md](VISION.md) | Who this is for, what it is not, and the principles that decide arguments |
| [docs/FEATURES.md](FEATURES.md) | Every feature, with a priority and a phase, plus the things we deliberately will not build |
| [docs/ROADMAP.md](ROADMAP.md) | Releases, exit criteria, and what "done" means at each stage |
| [docs/ARCHITECTURE.md](ARCHITECTURE.md) | Go, HTMX, SQLite, one op registry, auth, storage, security, budgets |
| [docs/DATA-MODEL.md](DATA-MODEL.md) | Tables, ownership, migration ranges, indexes |
| [docs/DESIGN.md](DESIGN.md) | Typography, colour, motion, the editor, mobile, accessibility |
| [docs/ACCESSIBILITY.md](ACCESSIBILITY.md) | The standard we hold ourselves to, what is checked by a script, the release checklist that has not been run, known gaps, how to report |
| [docs/AI.md](AI.md) | The AI writing partner, provider adapters, context, privacy, cost |
| [docs/RESEARCH.md](RESEARCH.md) | What we took from quire, weave and loom, and what we left |
| [docs/OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) | The decision log: what is settled and what is still open |
| [wireframes/README.md](../wireframes/README.md) | The Paper wireframes; earlier directions are archived at tag `archive/wireframes` |
| [AGENTS.md](../AGENTS.md) | The invariants an implementation must keep |
| [docs/WRITEON.md](WRITEON.md) | The source feature list from Write_On, preserved |
| [docs/AGENT-GUIDE.md](AGENT-GUIDE.md) | Giving an agent a scoped key, the CLI, and the generated skill |
| [docs/OPERATIONS.md](OPERATIONS.md) | Install, configure sign-in, proxy, backup and restore, upgrade, AI providers, Git sync, webhooks, MCP |
| [docs/DESKTOP.md](DESKTOP.md) | The desktop app: what it is made of, where it keeps things, what the window will and will not do, how to build it, and what has been verified |
| [docs/API.md](API.md) | The REST API: authentication, errors, concurrency, idempotency, examples, and every operation (generated) |

Review order for a first pass: VISION → FEATURES → ROADMAP. The rest supports those three.

---

## The bet

Most writing tools are either **note apps with a publish button** or **desktop word processors with a sync problem**. Neither holds a manuscript.

Scríobh is organised around the unit that actually matters: **the project**, with a manuscript inside it, a plan beside it, a cast of characters underneath it, and revisions of every word. Everything else — the editor, the AI, the exports — serves that structure.

Four commitments shape every decision:

1. **The writing surface is sacred.** Nothing animates, reflows, or interrupts while you type. AI never writes into the document without an explicit accept.
2. **Structure can wait, but it must not be an afterthought.** A blog post and a 120,000-word novel use the same parts, in different proportions.
3. **Private by construction.** Self-hosted, SQLite, no third-party calls except the AI providers *you* configure, and a context preview before anything leaves the box.
4. **Small enough to trust.** One binary, one data directory, no external queue, cache, or search service. If it breaks, `scp` and `sqlite3` are enough to fix it.

The baseline is **Paper** ([wireframes/](../wireframes/README.md)): warm paper, a moss accent, a manuscript tree, a 316 px inspector, and a shelf of typographic covers.

**Editorial first.** The editor, outline, entities, timeline, research, revisions, and compile are first-class. AI is second-class: it can be switched off, and the book must remain whole without it.

**Agents are guests with keys.** A scoped CLI (`scriobh-cli`, with name-based addressing, JSON output, `--dry-run` and idempotent writes) and a generated skill let an agent build a Compendium or draft a story externally, through the same operations a person uses. See [docs/AGENT-GUIDE.md](AGENT-GUIDE.md).

---

## Hard constraints

| Constraint | Consequence |
| --- | --- |
| Go | Standard library `net/http`, `modernc.org/sqlite` (no CGO), embedded templates and assets, single static binary |
| SQLite | One writer, WAL, embedded migrations. No Postgres mode. Scale target: a household, not a SaaS |
| OIDC only | No local passwords, no password reset, no TOTP. IdP groups decide roles. API keys for machines |
| Web | Server-rendered HTML + HTMX + small vanilla JS; one pinned ProseMirror editor bundle, no npm at runtime, no client-side router |
| Mobile web | The same application at 390px, with a real editing experience, safe-area handling, and a PWA shell |
| Privacy | No analytics, no external CDNs, no fonts from Google, no error reporting service. AI is opt-in per project |
| Self-hosted | One container or one binary, one data volume, documented backup and restore |
| Desktop | The same binary on a loopback port, with no hardcoded origin and no reverse-proxy dependency, wrapped by a Tauri shell with an embedded OIDC issuer (Phase 8); never a second auth path. Built and run unsigned on one Mac; see Status |

---

## What Scríobh is not

- Not a notes app. There is no infinite graph, no daily-note ritual, no plugin marketplace.
- Not a real-time collaborative document like Google Docs. Comments and suggestions, yes; live multiplayer cursors, not in v1.
- Not encrypted at rest. No encryption of the database, exports, mirror, or backups is implied or provided; protecting the volume is the operator's job.
- Not a publishing platform. It can export and generate a static site; it is not a CMS with a public audience and a comment section.
- Not an AI book generator. The AI is a partner at the desk, not a ghostwriter with the keys.
- Not multi-tenant SaaS. One deployment serves one household or one small team.

The full list, with reasons, is in [FEATURES.md § V](FEATURES.md#v-deliberate-non-features).

---

## The shape of the thing

```
Browser (HTMX shell + ProseMirror editor)
        │
        ▼
Go binary ── op registry ──┬── HTML fragments
   │                      ├── REST /api/v1
   │                      ├── CLI + API keys
   │                      └── MCP server (agents)
   │
   ├── SQLite (WAL)  data/scriobh.db
   ├── Files         data/files/…        images, covers, imports
   ├── Mirror        data/mirror/…       every project as plain Markdown
   ├── Git           push-only: mirror → a repo you own; import from a repo
   └── AI            openrouter │ opencode │ openai-compatible │ ollama
```

The mirror is the escape hatch: the database is canonical, but a read-only Markdown copy of every project is written continuously, so `grep`, `git`, and a text editor always see your work.

---

## Prior work in this repository

An earlier scaffold lived here: a WriteFreely-style public blogging app with local password auth, a SolidJS frontend, and public discovery. It conflicts with the current brief (OIDC only, private by default, long-form rather than blogging), so it is archived rather than deleted:

- branch `archive/writefreely-scaffold`
- tag `pre-plan-scaffold`

The SQLite layer and theme work are available there to salvage. Nothing in this plan depends on it.

---

## Licence

MIT. See [LICENSE](../LICENSE).
