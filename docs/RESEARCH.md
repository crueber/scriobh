# Research: quire, weave, loom

What we studied, what we are taking, and what we are leaving behind.

**Method.** quire was read from its repository (`README.md`, `SYNC.md`, `site/index.html`, the design prototype) and inspected live with REA against `https://bjarneo.github.io/quire/` (evidence `ev_923066b1c651ded7e87508a5151c2627f2c195abea64931520ce576782edf1e6`: 1,147 accessibility nodes, 23 regions, zero console errors). Weave and loom were read from their repositories, including `AGENTS.md`, `docs/DESIGN.md`, and the feature ledgers.

**Licensing.** quire is MIT. weave and loom are MIT. No code, templates, CSS, or assets are copied into Scríobh. Behaviour, naming conventions, and generic data-model ideas are not copyrightable, and where a pattern is distinctive we say where it came from. The design brief is deliberately different: a web app with a server-side manuscript model, not a desktop Markdown folder editor.

---

## 1. quire

A plain Markdown editor for folders of files, in Rust (desktop) and Kotlin (Android). Strong opinions: your notes are files, the editor is a text buffer, and the interface is quiet.

### What we adopt

| quire idea | In Scríobh | Why it survives the change of platform |
| --- | --- | --- |
| Markdown as the storage format, source visible | Yes. Markdown is stored; rich text is a view, with a Source toggle | Portability, grep-ability, no proprietary document model |
| Source mode + typeset preview (`Mod+E`) | A Markdown Source view beside a rich, book-like surface | Writers want to see the page, but want to trust the bytes |
| Focus mode with dimmed non-active paragraphs | Yes, plus typewriter scrolling | The single most requested "please make it quiet" feature in every writing app |
| Typewriter scrolling | Yes, opt-in | Keeps the eye in one place on tall screens |
| Fuzzy quick switcher (`Mod+O`) | Yes | Faster than a tree for anyone who knows the title |
| Command palette (`Mod+P`, we use `Mod+K`) | Yes, generated from the op registry | One navigation surface, discoverable, keyboard-first |
| Editor font, size, measure as settings | Yes, per device | Type is personal; a fixed face will lose users |
| Warm light theme and true dark theme | Yes, six themes, two designed first | The default look is a feature |
| Wallpaper-free, calm settings pages | Yes | Settings are where apps usually become ugly |
| `[[wiki links]]` with auto-create | Yes, in the manuscript and notes | Cheap, understandable, no graph database needed |
| Tags with counts | Yes, with a manager | Useful for research and themes, not as the primary structure |
| Outline of headings | Yes, plus scene-level synopsis | Manuscripts need both |
| Backlinks panel | Yes, Phase 3 | Continuity work benefits from "what links here" |
| Per-note lock (password) | No, but per-project AI exclusion and share links instead | A server that must render the text cannot honestly promise note-level encryption |
| Object-store sync with optional encryption | No, replaced by self-hosting + mirror + backup | The server *is* the sync target; adding S3 would double the storage model |
| MCP server, read-only by default, per-tool switches | Yes, Phase 5 | The right shape for agent access |
| Trash instead of delete | Yes | Non-negotiable for writing |
| Keyboard-first, no mouse required | Yes | See accessibility |

### What we adapt

| quire idea | Adaptation |
| --- | --- |
| A folder is the whole mental model | A project is the model; the tree is a view of it. Folders are one project kind, not the architecture |
| Notes are files, no database | Database canonical, continuous Markdown mirror. Files remain readable and greppable, but the server needs transactions, revisions, comments, and search |
| One note = one file, conflicts resolved by a conflict copy | Revisions + integer concurrency token + conflict copy only when both sides really changed |
| Read-only-by-default MCP with per-tool switches | Same default, with scoped API keys and an audit log |
| 14 themes | Six, contrast-checked in CI. More themes are a maintenance tax with diminishing returns |
| Per-note encryption | Out of scope on a server; documented honestly rather than half-promised |

### What we leave

- The Rust/Rune desktop shell and the native Android app. The web app is the product; a PWA covers the phone.
- The file-tree-as-only-navigation. A 90-chapter novel needs an outline, a board, and a timeline.
- The "no import, no export" purity. Scríobh imports DOCX and Markdown and exports five formats, because finishing a book is the point.
- The lock feature (see above).
- The 38-second demo video aesthetic. Not a technical decision, but the tone we want is plainer.

### The one sentence worth stealing

> "No database, no lock-in, no import step and no export step."

Scríobh cannot say the first clause and mean it. It can say the rest: the mirror means there is always an import and export step that already happened.

---

## 2. weave

A self-hosted kanban server in Go + HTMX + SQLite with OIDC-only auth, an op registry, and first-class agent access. Structurally the closest thing to what Scríobh needs.

### Patterns adopted wholesale

1. **One op registry.** `ops.Register[In,Out](Op{Name,Method,Path,Auth,UI}, fn)`. Every capability is an op; the browser, REST, and CLI are three renderings of the same set. This eliminates the "the UI can do it but the API cannot" drift and gives the authorization sweep test something to walk.
2. **OIDC only, groups → roles.** No local passwords, no reset flow, no TOTP. The IdP owns identity. `issuer|sub` binding with a one-time conditional email link, and an audit line on mismatch.
3. **Durable event stream + in-process hub.** Events are rows with a monotonic `seq`; SSE tails the table and treats the hub as a wake-up. Replay and live are one path. A slow consumer cannot lose events.
4. **Single replica, SQLite, WAL, short transactions.** The deployment story is one container and one volume. No Redis, no queue.
5. **Migration ranges per domain, never edit an applied migration.** A pre-flight backup before migrating.
6. **Security posture.** Strict CSP with no inline scripts, `allowEval=false` for htmx, CSRF via origin checks, body caps, rate limits, authz returns 404 for missing access, distroless non-root container with a read-only root filesystem.
7. **Schema-driven CLI and agent skill.** `weave skill install` teaches a dozen coding agents how to use the API. Scríobh will do a smaller version: an MCP server plus a `scriobh skill install`.
8. **Design constraints as rules, not taste.** "No gradients anywhere", "light and dark must both look great", "judge looks by reading screenshots from a real browser". Scríobh keeps all three.

### Patterns adapted

| weave | Scríobh |
| --- | --- |
| Boards → lists → cards | Projects → documents → scenes. The same nesting instinct, a different unit |
| Card claims with leases for agents | Single-writer document lease for humans and agents; no claim queue |
| Drafts/inbox for cards | Project inbox for fragments and shared text (Phase 6) |
| Saved filters, card templates, board templates | Saved searches, project templates, compile presets |
| Time tracking on cards | Writing sessions and sprints on documents |
| Webhooks with 50+ events | A smaller set: document, comment, export, project |
| Planka clean-room constraint | Not applicable; there is no reference product to clone |

### Patterns rejected

- **The kanban board as the primary surface.** Useful for scene status; wrong as the home page.
- **Agent accounts as non-SSO users by default.** Scríobh agents are API keys on a real principal, not a parallel user type, until there is a reason.
- **A 50+ event webhook taxonomy in v1.** Start with four.
- **Custom fields on every entity.** Entities get a `fields_json` bag; only documents get first-class columns.

---

## 3. loom

A minimal Go + SQLite boards app for links and notes. Small, fast, and full of hard-won interaction detail.

### Patterns adopted

| loom idea | In Scríobh |
| --- | --- |
| Blocks inside a card: link, note, todo, image | Research notes and project cards use typed blocks; documents stay prose |
| Inline editing, blur commits | Inline rename in the tree, inline entity fields, inline synopsis |
| Enter creates and stays focused; composers keep you typing | Chapter and scene creation, comment composer, entity aliases |
| Live sync never steals focus; a rebuild never strands the caret | Every htmx swap in the editor and tree; non-negotiable |
| Cache-first shell, server-stamped asset hashes, service worker | PWA shell, `?v=<hash>` assets, offline shell in Phase 3 |
| JS budget enforced by a script (≤50 KB gzip, measured ~38 KB) | ≤40 KB, checked in CI, with a first-paint probe |
| Optional OIDC with a first-user admin bootstrap | OIDC is required, but the bootstrap and "test connection" UX are borrowed |
| Per-board viewer/editor invites | Per-project roles: owner, editor, reviewer, reader |
| Import with dedupe, full export for backup | Import Markdown/DOCX, export everything, mirror on disk |
| Paper-like themes, per-column colour washes | Six manuscript themes, one accent, no washes on prose |
| Local favicon/icon caching instead of hotlinking | No external images at all; generated icons and covers |
| Confirm dialog instead of native `confirm()` | Native `<dialog>` with undo where possible |

### Patterns rejected

- **Open single-user mode by default.** Privacy and multi-user sharing need identity from request one; OIDC-only is simpler than a mode switch.
- **A JSON-file backend alongside SQLite.** One storage engine, one set of semantics.
- **Drag-and-drop as the only way to reorder.** Keyboard move commands exist everywhere a drag does.
- **Horizontal board scrolling as the main navigation.** The outline is vertical.

---

## 4. What none of them do

This is the gap Scríobh is built for:

| Missing elsewhere | Scríobh |
| --- | --- |
| A manuscript tree with parts, chapters, scenes, front and back matter | Core data model |
| Revision history that survives an edit, with diffs and named snapshots | Core data model |
| Compile presets and real DOCX/EPUB/PDF output | Phase 5–6 |
| Continuity: characters, places, timeline, mentions | Phase 3 |
| Shared compendiums and entity libraries across books | Phase 3, the Compendium |
| AI with a visible context payload and per-project privacy policy | Phase 4 |
| A writing-specific home: project progress, next scene, daily goal | Phase 2 |
| Reviewer workflow: anchored comments, suggestions, beta links | Phase 5 |
| Git-native sync: the manuscript as commits in a repository you own | Phase 2, push-only; import in Phase 3 |

quire syncs through object storage with an open, documented format; that is good work, but it is a second copy, not a history. Weave and loom have no sync story at all. A Git remote gives the writer versioned history, an external backup, and a place other tools already read, without making the filesystem a second writer.

quire is a beautiful notebook. weave is a beautiful board. loom is a beautiful pinboard. Scríobh is the desk where the book actually gets finished.

---

## 5. Novlr Writing Studio (earlier product)

The reference for the v1.2 writing-habit layer is the **earlier** Writing Studio, as shown in the screenshots supplied by the maintainer and stored under [`docs/reference/novlr-writing-studio/`](reference/novlr-writing-studio/):

| Image | What it shows |
| --- | --- |
| `01-goals.png` | Streak nudge (“450 words to go today”), a 2,500-word daily habit at 82%, a 4-week streak, a 24-week productivity grid with 100 active days, the words-written-today card, and a project-goals panel with 2 active / 1 completed, progress bars, and a “50% of your daily goal” nudge |
| `02-analytics.png` | Four period cards (today, 7 days, 30 days, 365 days) with a delta against the previous period and a sparkline; a habit-streak card with today’s words vs target and longest streak; a 24-week activity calendar with Mon/Wed/Fri rows, month labels, five intensity steps, and “32 writing days”; a “productivity trend” card; and a words-written line chart with a 30-day range |
| `03-habits.png` | The “Discover your writing habits” framing: word counts over time and the most productive moments |

**Take:** the four period cards with deltas and sparklines; the habit-streak card (current, longest, today’s target); the 24-week calendar with Mon/Wed/Fri rows, five steps, and a writing-days count; the project-goals panel with active/completed counts and per-goal progress; the words-written chart with a range selector; the gentle nudge copy (“450 words to go”, “50% of your daily goal”); weekly and monthly goals; progress emails.

**Adapt:** “productivity trend” becomes a quiet, non-judgmental comparison of the last three weeks with the previous three, with a link to resources rather than a warning. “Share” becomes a **private progress card** (PNG or private link), not a social post. Progress emails go through the instance’s own SMTP and are off by default. All analytics are computed locally from Scríobh’s sessions and revisions.

**Leave:** the current live Novlr site is not the reference. Cloud sync, social sharing, and any streak mechanic that shames a missed day are out. A missed day is a missed day.

The previous live-page summary was discarded after the maintainer supplied these screenshots; the features above are the ones that matter.

---

## 6. Source notes

- quire: `https://github.com/bjarneo/quire`, MIT, site at `https://bjarneo.github.io/quire/`.
- weave: `https://github.com/crueber/weave`, MIT, `docs/DESIGN.md` is the architecture contract.
- loom: `https://github.com/crueber/loom`, MIT, `FEATURES.md` is the interaction ledger.
- Write_on feature list: supplied by the maintainer, preserved in [WRITEON.md](WRITEON.md).
