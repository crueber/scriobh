# Compendium in the writing view: a sidebar panel first, a short way out second

Status: builds S, A, B and C are built (see FEATURES.md BI and BJ, F-1262 to F-1266, and OPEN-QUESTIONS.md for where B and C differ from this plan: the events view reads every Compendium of the book, a note is saved with a button, the way back is ranked from the caller's own state because `project.list` redacts a locked book's time, the browser's own scroll restoring is switched off for the writing page, the three views are one row of buttons, and the e2e is `scripts/e2e-compendium-nav.mjs`). Builds started after `split-chapter` merged to main.
Rows are numbered from F-1250 (F-1224 and F-1225 are taken by `split-chapter`): the shortcuts build takes F-1250 to F-1252, build A F-1253 to F-1261, build B F-1262 and F-1263, build C F-1264 to F-1266.

## 1. The goal, and what it changes

The owner's rule: the writer should not have to leave the writing page to do compendium work. Anything that is updated regularly lives in the right sidebar (the inspector). A trip to `/c/<slug>` is the rare case.

So the plan has four builds, in this order, each with its own review. Before them comes the undo and redo build, which is separate work and is not in this plan. I could not find it in the repository or its branches, so what it adds is unknown to me; build S only needs its key rows (3A.2).

| Build | What | Size |
|---|---|---|
| (undo and redo) | Someone else's build, first. | not planned here |
| **S** | **Keyboard shortcuts you can find**: a keyboard icon in the header, the `?` key and a palette command open one modal of every shortcut, generated from one registry that also feeds the palette hints, the toolbar hints and the desktop shell's chord list, with tests that fail when a chord is used and not documented. Ships before A so Alt+Shift+C is documented the day it exists. | small |
| **A** | A new **Compendium** tab in the inspector: look up, edit (including the long description), add a new entry from selected text, "Also known as" names (including from selected text), this book's version of an entry, point of view and location from an entry, scene names that open in the tab. The entry view and the entity list are redesigned for density in the same build, on the sidebar and on the full page, because they share the entry markup. | the main work |
| **B** | Same panel, more of the frequent work: events this scene tells, a quick research note. | medium |
| **C** | The rare trip: "Back to writing" on compendium pages, the Compendium tab opening `/c/<slug>`, the wiring page kept in the project menu, caret and scroll restored on return. | small |

The toggle chord from the first brief (writing view to compendium page and back) is **dropped**. Its budget goes to one chord that opens or focuses the sidebar panel (section 4.9). The `?from=<project>` override is dropped too (section 6.3).

Nothing in this plan adds an op. Every write goes through an existing op, so there is no `make skill` and no new authz sweep entry. Section 4.12 says how that was checked, and what to do if review finds one op missing.

## 2. What the code does today (facts the design rests on)

Read from `main` at 81f5b6a9, and from `split-chapter` at its current head c4197f86 for the overlaps (re-checked: between the first read at 16df9827 and c4197f86 only `docs.go` and its test changed, so `write.html`, `palette.js`, the chord B claim, the 3 KB `split.js` limit and the i18n prefix line are as cited here; re-check once more when it merges).

**Inspector.** `write.html` draws it: tabs Details, Versions, In this scene, Alternatives, then (editors) Stash, Comments, Lab, AI. `inspector.js` owns open, close, tab keys and the `scriobh_insp` cookie, so the server draws it open on the next page (no reflow after load). A tab that is its own file is named in `LAZY` and fetched on first show from `#write[data-<name>-src]`. The cookie value is whitelisted in `write.go` line 278. At 390 px it is a bottom sheet (`inspector.css`, max-height 72dvh, rides above the keyboard with `html.kb`). Left open on a wide screen it stays open from chapter to chapter; a phone always starts with it closed.

**"In this scene" (`scene.js`, JSON from `scene.entities`).** Shows point of view and place (with Change, Choose, Unlink through `scene.set_pov` and `scene.set_location`, which take the document revision), and the entities the text names, each with a card (the book's summary and an "Open the sheet" link that leaves the page). It cannot search, shows no fields or aliases, and edits no entity. It redraws itself (`replaceChildren`) 2.5 s after every save.

**Names in the text (`mentions.js`).** The editor underlines names and aliases. Hover or caret shows a card with a link; Ctrl or Command click and the palette's "Open the entity under the cursor" both `flushAll()` and navigate to `/p/<slug>/e/<id>`. `ask()` re-reads the marks and is already safe against typing and unsaved text.

**Entity ops that already exist** (all through the registry):
- Read: `project.entity.list` (q, kind, limit, offset; `Hidden` flag; `via` primary, reference or link; `overridden`; `editable`), `project.entity.get` (effective view, body, override and base), `entity.get` (the only one that returns `revision` and aliases with ids), `entity.field.list`, `project.compendium.get` (wiring), `compendium.list` (role and address).
- Write: `entity.create` (compendium editor), `entity.update` (compendium editor; `base_revision`, 409 carries the current revision), `entity.alias.add` and `.remove` (compendium editor; the 409 names the entity that holds the word), `project.entity.override.set` and `.clear` (project editor, caller must be on the entity's compendium; no revision), `scene.set_pov`, `scene.set_location`, `compendium.create` with `project_id` (project owner).
- Visibility: `project.entity.list` and `.get` already return only entities of compendiums the caller is on (F-476). Project access never grants the bible.

**Project-local entities (F-473)** are schema only. No op writes or lists one, and `via` has no such value. The panel cannot show them. It renders whatever `via` says and has a fallback tag, so the day the ops land the panel needs only the label.

**Aliases (F-116) already work** and are on the full entity page under "Aliases" (`entity.html` lines 50-61). A user did not find them. The panel puts them right under the name as "Also known as", and the full page is relabelled (section 4.8).

**Does reopening a document restore caret and scroll? No.** `doc.js` stores nothing for either. The editor takes focus on load only for an empty chapter on a fine pointer. The page is `Cache-Control: no-store` and the editor replaces the plain text after load, so even the browser's own Back-button scroll restore is unreliable. This matters for build C and for reloads, not for build A (the sidebar never navigates).

**Leaving the page flushes unsaved text** through `doc.js` `go(href)`: `flushAll()`, then `location.assign`, and it stays put with a toast if the save fails. Any click on a same-origin `a[href]` goes through it when text is unsaved, and `S.write.go` is what the palette calls. `beforeunload` asks if text is still unsaved. A sidebar form is not covered by any of this (section 4.5 handles it).

**Chords.** Claimed in `S.chords` (created by whichever file loads first). Taken: Alt+Shift+A N P O G S (alts.js), I and J (aiboot.js, ai.js), B (split.js, from `split-chapter`). `e2e-ai-edit` fails if `chords.clash` is not empty. `alts.js` is lazy (after load) and 593 B under its limit; `inspector.js` is also lazy, loaded on every writing view, and is the right home for a chord that opens the inspector.

**Where shortcuts are written down today (the reason for build S).** There is no help page and no icon: `ACCESSIBILITY.md` says the generated reference (`?`, F-166) is "planned and not built". The chords are spelled out by hand in at least six places that nothing compares: `S.chords` (claimed in `alts.js`, `aiboot.js`, `ai.js`, and `split.js` after the merge); the palette's `key:` strings (`palette.js`, for example "Alt Shift I"); the toolbar buttons' `data-keys` attributes (`write.html`); the editor keymap (`web/editor/src/rules.js`: Mod-z, Shift-Mod-z, Mod-y, Mod-b, Mod-i, Mod-Shift-x, Mod-k, Mod-e, Mod-s, Mod-Enter, Mod-Alt-0 to 3, Shift-Enter, Tab); the per-page key handlers (`tree.js`, `outline.js`, `boards.js`, `timeline.js`, `research.js`, `read.js`, `review.js`, `share.js`); and **`desktop/src-tauri/src/menu.rs` `APP_CHORDS`**, a Rust list of every chord the web app listens for so a native menu accelerator never shadows one (`split-chapter` already had to add three entries to it). A new chord must be added to all of them from memory.

**Patterns looked at for the help modal (read-only, public sources).**
- Weave (`crueber/weave`, public): `boards_board.html` carries a hand-written `<dialog id="boards-help">` titled "Keyboard shortcuts", with `<h3>` groups (Cards, Board, Card detail, Composers) and `<dl>` rows whose terms are `<kbd>` caps. `boards_keys.js` opens it with `showModal()` on `?` (ignored while typing in an input, a textarea or a contenteditable) and from a header keyboard-icon button (`data-help`). The button is hidden on a coarse pointer and under 640 px; `kbd` has a hairline border and a thicker bottom border; two columns, one under 640 px. Strength: icon, `?` and a grouped modal. Weakness: the dialog is typed by hand beside the key handlers, so it can drift.
- Fizzy (`basecamp/fizzy`, public): a key is declared on the element it triggers (`data-action="keydown.k@document->hotkey#click"`), and `HotkeysHelper#hotkey_label` renders the label per platform (a `ctrl` token becomes the Command sign on a Mac) next to the control. Strength: the key sits with the thing it does, and labels follow the platform. I did not find a central help modal in Fizzy's source.
- Taken from both: Weave's icon, `?` and grouped modal; Fizzy's per-platform labels from one neutral token form (`Mod`); and, beyond both, one registry so the modal cannot drift.

**Why the entity pages feel sparse (the reason for the density work).** From `entity.html`, `entity-fields.html`, `entities.html`, `entity-list.html`, `entities.css` and the base sheet:
- The sheet sits on `.settings`, 640 px wide (`.ents` 720), and every part is its own `<section>` with `--s-7` (48 px) above it, a 28 px h1, an h2 and usually a "sub" paragraph: Details, Aliases, Relationships, Where this appears, Use in a project, Merge and trash. Six headed blocks before the page ends.
- Fields are stacked: a label line (18 px), a 6 px gap, and a 40 to 44 px input, then the form gap, about 80 px per field, so ten fields are 800 px of scrolling.
- Which fields exist is hidden: filled fields are shown and the empty ones sit inside a closed `<details>` "Fields with nothing in them yet". A writer cannot see that "Fears" or "Voice" exists.
- The top of the page is spent on Name, Sort name, a hint paragraph, Summary, a hint paragraph and a Colour row before the first field. Name and sort name are set once; the summary and the fields are what writers touch.
- The notes box is a 12-row monospace textarea, always open, with its preview button and region.
- Aliases are a form of their own (a field, a second "where it comes from" field, a button) under a heading with a lede, after the details. Relationships have a five-field form.
- No section index, no counts, nothing says that there are four more sections below.
- The list: each entity is a row with 12 px padding, a 44 px name link, a kind chip, a summary line and an aliases line, about 100 px a row, so about six on a 1280 by 800 screen.
- At 390 px the same blocks simply stack; nothing is denser.

**Budgets measured on main** (gzip, as the tests count them):

| File | Now | Limit |
|---|---|---|
| `doc.js` + `tree.js` | 21,912 (after `split-chapter`: about 16 B under) | 22,016 |
| `inspector.js` | 5,000 | 5,120 |
| `scene.js` | 3,622 | 4,096 |
| `mentions.js` | 4,493 | 5,120 |
| `palette.js` | 7,216 | 12,288 |
| shell JS total | 31,547 | 40,960 |
| writing page CSS on load | 22,332 | about 25,600 |

`doc.js` and `tree.js` are not touched by any build below. That is a hard rule, not a goal.

## 3. Frequent or occasional

The split follows one question: does a writer do it in the middle of a scene, more than once a week, without wanting to leave the sentence?

| Action | Where | Why |
|---|---|---|
| Look up an entry by name, alias or word | **Sidebar** | The first reason anyone leaves the page. |
| Read the whole entry (summary, fields, body, also known as) | **Sidebar** | Same. |
| Edit summary, fields, description of an entry | **Sidebar** | "Her eyes are grey" is a mid-scene fact. |
| Add another name ("Also known as") | **Sidebar** | A name is invented in the prose, then it needs to count as a mention. |
| Add selected text as another name for an entry | **Sidebar** | The natural way "Mrs Riviera" comes about. |
| New entry from selected text | **Sidebar** | A name just invented, minimum fields (name, kind, optional summary). |
| This book's version (override: "age at this point in the story", notes) | **Sidebar** | The thing a shared bible exists for. |
| Set an entry as the scene's point of view or location | **Sidebar** | Already a sidebar action; the entry view gets the button. |
| Names the scene uses, jump to the entry | **Sidebar** (scene tab hands over) | |
| Events this scene tells; add one | **Sidebar, build B** | Frequent for writers who keep a timeline; not for all, so second. |
| Quick research note | **Sidebar, build B** | A note jotted mid-scene; browsing and organizing notes stays on the page. |
| Create a Compendium for a book that has none | **Sidebar** (empty state, project owner) | First-run need; one button. |
| Relationships between entities | Full page | Needs a second entity and labels; rare, deliberate. |
| Colour, sort name, custom field definitions | Full page | Set once. |
| Trash, restore, merge, split, change primary, purge | Full page | Destructive or structural; a preview and a typed confirmation do not belong in a 360 px panel. |
| Members, roles, export, import | Full page | Administration. |
| Wiring (primary, references, links) | Full page, reached from the project menu | Rare. |
| Research board, tags, moves, clippings with source details | Full page | Organizing, not jotting. A clipping's address and date are typed deliberately. |
| Timeline editing in depth (participants, places, narrative order) | Full page | |
| Compendium scratchpad | Full page | A free-form pad shared by the compendium's members, opened to read at length; a quick note in build B covers the jotting. |
| Attachments of a Compendium (upload, rename, delete) | Full page | Files are chosen and checked deliberately; the writing view's own Add image button already handles a picture inserted in the text. |
| "Where this appears" for an entry | Full page, link from the entry | A report, not a lookup. |

Every entry in the sidebar carries "Open the full page" (and the book's own sheet), so the rare path is one click away.

## 3A. Build S: keyboard shortcuts you can find

The owner: nowhere in the application makes it obvious what the keyboard commands are. Build S makes them findable, correct and impossible to forget when a new one is added.

### 3A.1 What the writer sees

- **A keyboard icon button** in the header (`topbar-right`, before the palette hint), on every signed-in page, so the writing view's header has it and so do the library, the Compendium pages and settings. Label "Keyboard shortcuts", `aria-keyshortcuts="?"`. On a coarse pointer or under 640 px it is hidden (no keyboard to learn), as Weave does; the same entry is a line in the account menu (all sizes, so a phone with a hardware keyboard has it) and a palette command **Keyboard shortcuts**.
- **The `?` key** opens it from anywhere that is not a text field, a text area, a select or the editor (a `?` in the manuscript is a question mark, so the editor keeps it). Inside the editor the way in is the palette (Mod+Shift+P or Mod+/, then "Keyboard shortcuts") and the header icon. No new chord is spent on it.
- **The modal** is the page's existing `#dialog` (so it gets the shell's behaviour: `showModal()`, background inert, Esc closes, focus goes back to the control that opened it, an expired session never swaps a sign-in page into it). Title "Keyboard shortcuts", a Close button, a one-line lede ("Ctrl on Windows and Linux, Command on a Mac. In the editor, `?` is a question mark, so open this from the palette."), and the groups below as `<section>` with `<h3>` and a `<dl>` of key caps and words. The group for **the page you are on** is first and open; the others follow, in two columns on a wide screen and one at 390 px, scrolling inside the dialog (max-height 85dvh), no horizontal scroll.
- **Key caps for this computer.** The registry stores neutral tokens (`Mod+Shift+Z`, `Alt+Shift+B`). The browser draws them per platform, one `<kbd>` per key: Command, Shift and Option signs on a Mac, "Ctrl", "Shift", "Alt" elsewhere; Delete versus Backspace where the tree differs. The palette's hints use the same function.

### 3A.2 The groups and the rows they start with

Each row is a registry entry (3A.3); the words are catalogue keys. Rows that exist today:

| Group | Rows |
|---|---|
| **Writing and format** (writing view) | Bold Mod+B; Italic Mod+I; Strikethrough Mod+Shift+X; Headings Mod+Alt+1, 2, 3 and plain paragraph Mod+Alt+0; Link Mod+K; Scene break Mod+Enter; Line break Shift+Enter; Markdown source Mod+E; Save now Mod+S; Indent and outdent a list Tab and Shift+Tab |
| **Undo and redo** (writing view) | Undo Mod+Z; Redo Mod+Shift+Z and Mod+Y (the editor's current keymap). If the undo and redo build adds toolbar buttons, a different redo key or a note about what is undone, it adds its rows here in its own commit, and the drift tests below fail until it does |
| **Alternatives and ghosts** | Add an alternative Alt+Shift+A; next wording Alt+Shift+N; previous Alt+Shift+P; original wording Alt+Shift+O; ghost or revive Alt+Shift+G; send to the stash Alt+Shift+S |
| **Writing partner** (shown when AI is on for the project) | Menu on the selection Alt+Shift+I; go to the waiting proposal Alt+Shift+J; in a proposal: A accept, L keep as alternative, R reject, T try again |
| **Chapters** | Split chapter here Alt+Shift+B; previous and next chapter Alt+PageUp and Alt+PageDown; go to a chapter or project Mod+O; show or hide the manuscript Mod+\ |
| **Compendium** | (added by build A) Look up, add a name, new entry: Alt+Shift+C; Esc back to the text; Enter opens the first result |
| **Palette and help** | Command palette Mod+K (Mod+Shift+P or Mod+/ inside the editor); Keyboard shortcuts `?`; Esc closes a dialog, panel or preview |
| **Manuscript tree and outline** | Arrow keys, Home, End; Enter opens; F2 rename; Delete trash; Alt+arrows move and indent; a letter jumps to a title; the menu key or Shift+F10 |
| **Corkboard, status board, research board, timeline** | Alt+arrows reorder; Left and Right change a card's status or column; Alt+Up and Alt+Down move an event |
| **Reading and review** (F-701) | Left and Right or J and K turn pages; `]` and `[` place the cursor in the next or previous paragraph; Shift with arrows selects; C comment; S suggest; M margin; J and K between threads; Enter shows in the text; R reply; X resolve; N next unresolved; Esc leaves |

The rows come from the handlers read for this plan; the build checks each against its handler and corrects the table, which is what the tests in 3A.4 are for.

### 3A.3 One source of truth

A **registry in Go**, `internal/shortcuts/registry.go`, a table of entries: `ID`, `Group`, `Scope` (where it works: `write`, `read`, `tree`, `outline`, `board`, `compendium`, `app`), `Keys` (a list of alternative token sequences), `Words` (a catalogue key `kb.<id>`), `Needs` (`ai`, `edit`, or empty), `Claim` (for an Alt+Shift chord, the `KeyX` code that `S.chords` must hold), and the F-number it belongs to. It is the only place a key is written.

It feeds:
1. **The modal.** `GET /ui/shortcuts?scope=<scope>&ai=<0|1>` renders the fragment from the registry with `T`. It reads nothing of a project, so it is listed in `notProject` of the gate sweep ("a static list"). `Cache-Control: private, no-cache` (the words follow the person's language).
2. **The palette's hints and the toolbar's `data-keys`.** A template function `{{keys "split"}}` writes the token string in `write.html`; `palette.js` reads a small JSON that the layout puts on the palette dialog (`data-keys`, id to tokens, only the commands the palette shows, under 1 KB gzip on every page) instead of its hand-typed `key: "Alt Shift B"` strings. A palette hint can no longer differ from the modal.
3. **The desktop shell's `APP_CHORDS`.** A Go test reads `desktop/src-tauri/src/menu.rs` and requires that its `APP_CHORDS` list equals the registry's modifier chords (Mod and Alt+Shift) after the `Mod` to `CmdOrCtrl` mapping. Adding a chord means adding it to the registry and to that Rust list; forgetting either fails the Go test, naming the missing chord.
4. **The docs.** The writing-view line of `ACCESSIBILITY.md` becomes a pointer to the modal, and F-166 (the generated reference) becomes built. No hand-kept chord list remains in prose.

The alternative considered, a JSON in the catalogue that the scripts claim from, would put words and keys in one file but leave the Go side (templates, the desktop test) reading JSON. A Go table gives the templates, the tests and the fragment one typed source, and the scripts only ever need the id-to-keys JSON of item 2.

### 3A.4 Tests that stop drift

- **A chord that is claimed but not documented fails.** `TestEveryChordIsDocumented` scans the shipped scripts for chord claims (`chords.KeyX =`, `claim("KeyX"`, the `KEYS = {…}` table of `alts.js`, `e.code === "KeyX"` beside `altKey && shiftKey`) and the editor's keymap (`web/editor/src/rules.js`), and requires each to be a registry entry; it also requires each registry Alt+Shift entry to be claimed somewhere. `format.js`'s synthetic Mod+K event is marked `// chord-ok`. The per-page handlers (tree, outline, boards, timeline, research, read, review) are checked by a smaller scan of their `e.key` tests against the registry's rows for their scope, and the build lists in its review any key handler the scan cannot see.
- **Words.** Every registry row has its `kb.*` key in the catalogue; group names too; no row without words.
- **Hints.** The palette script and the templates contain no hand-typed key strings (`key: "` and a literal `data-keys="` are scanned for), so the only way to show a key is the registry.
- **Desktop.** `APP_CHORDS` equals the registry (3A.3, item 3).
- **The fragment.** Anonymous gets the sign-in redirect; a signed-in person of any role gets it; `scope=write` puts the writing group first; `ai=0` leaves the writing-partner rows out; every row shown has a registry entry; no inline script or style.
- **End to end (`scripts/e2e-shortcuts.mjs`, added to `e2e-fast`).**
  - The icon is visible and named at 1280 px and absent at 390 px, with the account-menu entry present and working.
  - `?` opens the modal from the library and from the outline, and in the editor it does **not** open it and types a question mark.
  - The palette command opens it from the editor; Esc closes it and focus returns to the opener; Tab and Shift+Tab stay inside it.
  - With a Mac user agent the caps are the Command, Shift and Option signs, and elsewhere Ctrl, Shift and Alt.
  - At 390 px, opened from the account menu: one column, scrolls inside, no horizontal overflow, 44 px targets.
  - Both themes (screenshots in Paper and Ink; `scripts/check-contrast.py` reads tokens only, so the modal uses tokens and no new colour).
  - Every chord of the writing group is pressed in a writing view with AI on and its claim is found in `scriobh.chords` (the existing clash check stays empty), and every key sequence in the modal for `write` is a registry entry.
- **Existing scripts that must stay green:** `e2e-phase2` and `e2e-library` (the palette and the header; hint strings change from "Alt Shift B" to the platform form, and those scripts are updated in the same commit), `e2e-alts`, `e2e-alts2`, `e2e-ai-edit`, `e2e-split` (after it merges), `e2e-a11y` (a new header control on every page, both themes, 390 px, keyboard), `e2e-i18n`, `e2e-desktop` and `scripts/desktop-smoke.sh`, `e2e-write`, `scripts/csp-smoke.mjs`.

### 3A.5 Files, words, budget, rows

**Files.** New: `internal/shortcuts/registry.go`, `internal/shortcuts/registry_test.go`, `internal/server/shortcutsui.go` (the route, `{{keys}}`, the per-scope view), `web/templates/partials/shortcuts.html` (the fragment), `scripts/e2e-shortcuts.mjs`. Changed: `web/templates/layout.html` (the icon, the account-menu item, `data-keys` on the palette dialog), `web/static/js/palette.js` (the `?` key, the opener, the platform caps, hints from the registry, the command), `web/static/icons/sprite.svg` (a `keyboard` symbol in the sprite's stroke style), `web/static/css/app.css` (`kbd` caps and the dialog grid, about 0.4 KB gzip on the base sheet), `web/templates/write.html` (`{{keys}}` in place of literal `data-keys`; `split-chapter` edits the same buttons), `internal/server/gate_sweep_test.go` (a `notProject` entry), `internal/i18n/locales/en.json` (about seventy `kb.*` words), `docs/ACCESSIBILITY.md`, `docs/FEATURES.md`, `docs/ARCHITECTURE.md`, `Makefile`. `desktop/src-tauri/src/menu.rs` changes only if the new test finds a gap.

**Words (i18n).** `kb.title` Keyboard shortcuts; `kb.lede`; `kb.close`; `kb.here` On this page; group names `kb.g.write`, `kb.g.undo`, `kb.g.alts`, `kb.g.ai`, `kb.g.chapters`, `kb.g.compendium`, `kb.g.app`, `kb.g.tree`, `kb.g.boards`, `kb.g.read`; one `kb.<id>` per row (bold, italic, strike, h1 to h3, paragraph, link, scene_break, line_break, source, save, list_indent, undo, redo, alt_add, alt_next, alt_prev, alt_orig, ghost, stash, ai_menu, ai_proposal, ai_accept, ai_keep, ai_reject, ai_retry, split, chapter_prev, chapter_next, switcher, manuscript, palette, help, esc, tree_*, board_*, event_move, read_turn, review_*); `kb.js.none` for the one message the opener shows if the fragment cannot be fetched; `nav.keys` for the account-menu item and the icon's label.

**Budget.** No new script file: `palette.js` is on every signed-in page and has its own 12 KB limit. It grows by about 0.7 KB gzip (the `?` handler and opener 0.35, the platform caps 0.2, the hints read from the registry JSON 0.1, the command 0.05), from 7,216 to about 7,900; the shell total from 31,547 to about 32,250 (limit 40,960). The modal's content is **lazy**: the fragment (about 6 KB of HTML, 1.5 KB gzip) is fetched on first use and kept in memory for the page, so a page that never opens it pays nothing. `app.css` grows 0.4 KB on every page (the writing page from 22,332 to about 22,730 of about 25,600). Server: the fragment p95 at most 20 ms. `doc.js` and `tree.js` untouched. If `palette.js` passes 8.5 KB, the opener and caps move to a lazy `shortcuts.js`; not expected.

**Rows.** F-1250 the shortcuts modal and its entry points (header icon, `?`, palette, account menu), grouped, in this computer's keys, usable at 390 px; F-1251 one registry feeding the modal, the palette hints, the toolbar hints and the desktop chord list, with the drift tests (F-166 built); F-1252 the undo and redo shortcuts documented in it with the other writing keys.

## 4. Build A: the Compendium tab

### 4.1 A new tab, not an extension of "In this scene". Why.

- The scene tab redraws itself after every save. Forms in it would be clobbered while the writer types in them (invariant 8, "live updates never clobber unsaved input"). A new tab owns its DOM and replaces nothing that has focus or unsaved input.
- The scene tab is JavaScript building JSON; the owner wants server-rendered HTMX fragments, so the entry views reuse the full page's field rendering and wording.
- The scene tab answers "who is in this scene". The new tab answers "what do I know about X". Different questions; the scene tab stays and **hands over** to the new tab (4.7).
- The tab strip gets one more tab (nine for an editor). `.insp-tabs` has no overflow rule today; build A adds `overflow-x: auto` to it and the e2e checks it at 390 px.

Label: **Compendium** (the word the owner and the project strip use). If it does not fit at 390 px the review may shorten it to "Bible", the phone strip's word; that is a CSS-and-catalogue change.

### 4.2 Where it lives, and staying open across chapters

- `write.html`: a tab `itab-compendium` and panel `ipanel-compendium` after "In this scene", drawn for every project member (the content decides what they may do), plus `data-compendium-src` (script) and `data-compendium-css` (sheet) on `#write`.
- `inspector.js`: `panels.compendium`, `LAZY.compendium = "lookup"` (so the existing `lazyPanel` fetches `js/lookup.js` on first show, same as scene and stash).
- `write.go`: add `compendium` to the `scriobh_insp` whitelist, so a writer who left the tab open gets the panel drawn open on the next chapter on a wide screen, before the editor mounts: no reflow after load.
- The panel's content arrives after load into a fixed-height scroll area (`.insp-body`), so the editor never moves. The e2e measures the paper's box before and after the first fragment.
- What the writer was doing is kept per tab in `sessionStorage` (`scriobh:lk:<person id>:<project id>`: the last query, the open entry, the scope switch; the person's id is in the key, as the draft keys do, and sign-out already clears `scriobh:` keys it knows, so the key is added to its list in `shell.js`' sign-out clean-up only if that list is not a prefix match, checked at build). The next chapter's panel re-opens on it. On a phone the sheet starts closed, as every inspector tab does.
- A new tab load is one fragment request (list or entry); nothing is fetched until the tab is shown.

### 4.3 Routes and fragments

All under `/ui/projects/{id}/` so the project page gate (`projectScoped`) already covers them and `TestEveryRouteIsClassifiedForTheGate` needs no entry. Two patterns only, so `gate_sweep_test.go`'s `pageFill` needs no new parameter:

`GET /ui/projects/{id}/lookup` (auth check as `uiSimilar`, project through `goalsProjectByID`, so a trashed or unreadable project is 404). Query: `q`, `kind`, `offset`, `entity` (open one entry), `scope` (`record` or `book`), `sel` (selected text from the editor), `alias` (selected text to be added as a name; the rows become targets), `new` (open the create form with this name), `fresh=1` (entry re-read for "Use theirs"). One handler, one template file `partials/lookup.html`, several named fragments:

| Fragment | When | Content |
|---|---|---|
| `lk-empty-none` | `project.compendium.get`: no primary, no reference, and `project.entity.link.list` is empty | "This book has no Compendium yet." Project owner: **Make one for this book** button (`POST lookup/make`, `compendium.create` with `project_id`, starter fields). Others: "Ask the owner." |
| `lk-empty-closed` | decided from `project.compendium.get`, not from the list: `HasPrimary` is true (or a reference exists in `Hidden`) and the caller sees no primary and no readable reference. A member of an empty primary Compendium gets the normal list with its own "no entries yet" line instead, so the two cases are never confused | "This book's Compendium is not shared with you. Its owner can add you." Names nothing: no compendium name, no entity, no count. |
| `lk-list` | normal | Search field (type=search, label "Find in the Compendium"), kind filter, up to 30 rows (name, kind, `via` tag, "This book's version" tag, first line of summary), "More" button for the next page, an "Entries you cannot see are left out" line when `Hidden`. Result rows are buttons (`hx-get` the entry). |
| `lk-selection` | `sel` or `alias` present | The selected words as a quoted line; **Add as another name for…** (switches rows into targets), **New entry**; an exact match banner ("Already a name of Alexa Riviera. Open it."); likely entries from each word of the selection (up to 4 words of 3 or more letters, one `project.entity.list` call each, merged and ranked by how many words matched). |
| `lk-entry` | `entity` present | Section 4.4. |
| `lk-new` | `new` present | Name (prefilled), kind (five radios, no default selected when the selection gave no hint), summary (optional), **Create**. Shown only if the caller may create (4.6); otherwise a plain sentence of why. |
| `lk-status` | every POST | One `aria-live="polite"` line (Saved, Could not save, conflict box). |

`POST /ui/projects/{id}/lookup/{action}` through `s.uiRoute` (session, CSRF). Actions: `save` (record), `override`, `override-clear`, `new`, `alias`, `unalias`, `make`. Replies are fragments; 422 swaps (the page's htmx config already swaps 422 without raising an error).

Errors are plain words from the catalogue. A 404 from an op (an entity of a compendium the caller is not on) is the same page as for an id that does not exist.

### 4.4 The entry view

Order, top to bottom, all inside one scroll area:

1. **Back to results** (button) and the entry's name as the heading (`tabindex="-1"`, the focus target after an explicit open), kind tag, `via` tag ("Primary Compendium", "Referenced: <name>", "Linked entry"), "This book's version" tag.
2. **Also known as** (4.8): chips, each with Remove (editors of the compendium only), one field **Add another name** with Enter to add, and **Add the selected words** when the editor has a selection.
3. Scope switch, a two-button group `This book | Everywhere` (`aria-pressed`). *Everywhere* edits the Compendium's record; *This book* edits the override. Default: Everywhere when the caller can edit the record and the entry is `via=primary`; otherwise This book when the caller is a project editor; otherwise neither (read-only). The choice is remembered per tab.
4. The fields grid, then the Description, for that scope (4.4A and 4.5).
5. **Use as point of view** (characters) and **Use as location** (places), for project editors (4.7).
6. Links: **Open in the Compendium** (`/c/<address>/e/<id>`), **This book's sheet** (`/p/<slug>/e/<id>`), **Where this appears** (the same address with the section anchor). These are plain links and go through `doc.js`' click handler, so unsaved text is flushed exactly as a tab click does.

The layout of these parts (compact header, two-column field grid with quiet slots for empty fields, the Description as an open first-class section, counted collapsible sections) is 4.4A; this list is the order and the content. A person who may not edit sees the same grid as text, and the Description rendered through `text.RenderHTML` (heading shift 2, as the full page does).

### 4.4A Information density: the entry view, the full page and the lists

The owner: the Compendium should be more information dense, and should make it clear that more fields are available; today a writer scrolls a lot to find the detail to work on. The cause is in section 2. The remedy is one shared entry component used by the sidebar and the full page, and a compact list row.

**One entry component, two shells.** A partial `partials/entry.html` renders the entry for both: the sidebar wraps it in its autosaving form (`lookup/save`), and `entity.html` wraps it in the existing form that posts to `/ui/entities/{id}/save`, with the Save button kept (the page works without script, and its tests post the form). Both read the same view struct, so the two cannot drift. The element ids the tests use (`es-body`, `es-summary`, `ef-<key>`, `details`, `aliases`, `relations`, `where`) are kept.

**Inline edit, no edit mode.** Every value is its own input, always editable by someone who may edit. There is no Edit button and no read-only page to toggle out of (a person who may not edit gets the same grid as text). The sidebar saves by itself; the full page keeps its explicit Save (sticky, as today) because it is a whole-page form with a revision. If review wants autosave on the full page too, it is the same `lookup.js` saver pointed at `/ui/entities/{id}/save` and can follow later.

**The layout, top to bottom (the same on both):**
1. **A compact header, three lines at most.** Line 1: colour swatch, name (an h1 on the page, an h2 in the panel), the kind and `via` tags. Line 2: **Also known as** chips with the quick-add field inline as the last chip. Line 3: the summary, an auto-sizing textarea (two lines, grows to its text), quiet until hovered or focused. Sort name and colour move out of the header into a **Display** disclosure at the end of Details (set once).
2. **Fields in a tight two-column grid.** `dl`-style grid, label column 28 percent (min 5.5 rem), value column the rest; one line per field (about 36 px on a fine pointer, 44 px on touch); a long text value is a textarea of one line that grows. Gap 4 px. Ten fields take about 360 px, not 800.
3. **Empty fields are quiet slots, not hidden.** An empty field is shown in its place in the grid with a muted placeholder, "Add", and a hairline under it; it is an input, so click and type fills it. When a kind has more than eight empty fields, the first six are shown and a row **Show N more fields** (a native `<details>` styled as a row, so no script, with the count in its summary) holds the rest. The header of the section says **Fields (4 of 11 filled)**, so the writer sees at once that there are seven more.
4. **Description as a first-class section**, open by default: label **Description**, a growing textarea (min 6 lines, `field-sizing: content` where supported, otherwise rows 8), a Preview toggle for the Markdown (the same `/ui/md-preview` route the full page uses), a word count in the section header. It is not in a disclosure and not at the foot.
5. **Collapsible sections, each with a count in its summary line**: **Relationships (3)**, **This book's version (set, or none)**, **Appears in (5 scenes)** (full page only; the report stays a page), **Use in a project** and **Merge and trash** (full page only, closed). In the sidebar the first three are `<details>` rows under the Description; each opens in place. State is remembered per tab in `sessionStorage` (open or closed).
6. **A section index** on the full page: one row of anchor links under the header, `Fields 4/11 · Description · Relationships 3 · This book · Appears in 5 · Manage`, each a link to its section (`#fields`, `#description`, ...); sticky below the header on a wide screen, scrolling under the tab strip at 390 px. In the sidebar the summary rows are the index (it is narrow, and an index there would cost more space than it saves); `Tab` reaches each.

**Reading as well as editing.** The summary, fields and description are visible without opening anything; the closed sections show only a line each. On the full page at 1280 by 800, an entry with nine fields, three names, two relationships and a 200-word description fits with its sections visible and the first heading of "Appears in" in view.

**The lists.** `entity-list.html` and `project-entity-list`: one compact row, `[swatch] Name  kind — summary…  also known as…`, a single line truncated with an ellipsis on a wide screen (the full summary is the link's `title` and one click away), 36 px on a fine pointer; at 390 px two lines (name and kind, then the summary truncated), 56 px. The match line ("Matched in the notes: …") still appears as a second line, only for a search hit. Kind chips stay (they carry meaning without colour). The "Also:" line becomes "Also known as:" inline, truncated. About 20 rows on a 1280 by 800 screen against about six now. The sidebar's result rows use the same row. Paging is unchanged (`entityPageSize` is kept; a test counts `ent-row`).

**House style.** No gradients: flat fills, hairline rules (`--line`, `--line-2`), one accent, tokens only, both themes designed; focus rings as now; every state is words as well as colour; no animation (open and close of a `<details>` is instant; `prefers-reduced-motion` is already honoured). Contrast: the new sheet uses existing token pairs only, so `scripts/check-contrast.py` runs unchanged and must pass; the one new pair worth naming is the placeholder "Add" (`--muted` on `--surface`), which the script's muted rule already covers; a muted placeholder is allowed to be quiet but is checked as text.

**390 px.** The grid keeps two columns down to 360 px (label column 28 percent, min 5.5 rem; a label that does not fit wraps); below that, the label sits above the value. The section summaries are 44 px targets. In the sheet (inspector) the entry scrolls inside the sheet.

**Acceptance: before and after screenshots, and numbers.** The build's first commit takes the **before** set from main with `scripts/screens-entities.mjs` (extended to write named files), stored in `docs/screens/entities-before/` for the review: the full entity page of a character with nine defined fields (four filled), three names, two relationships and a 200-word description, at 1280 and 390, Paper and Ink; the entity list with 30 entries at both widths and themes. The last commit takes the **after** set under the same names, plus the sidebar entry at 390 (as a sheet) and at 1280. The review compares them side by side. `e2e-sidebar.mjs` asserts the numbers so they cannot regress: for that entity at 1280 the page's scroll height is at most 60 percent of the before value; all eleven field labels are visible (filled or as slots) without opening anything; at 1280 by 800 the list shows at least 15 rows; at 390 by 844 at least 8; the "Show N more fields" count equals the number of empty fields beyond the sixth. These thresholds are set from the measured before and after at build and written into the script with the numbers.

**Words that change with it.** The full page's "Notes (Markdown)" becomes **Description (Markdown)** (the id `es-body` and the field name `body` stay; `csp-smoke.mjs` and `e2e-entities.mjs` fill `#es-body`), and the override's "Notes" becomes **Private notes for this book**, so the two are never confused.

### 4.5 Editing: autosave, conflicts, and what never moves

**What can be edited, by scope.**
- Everywhere (the record): summary, every field the compendium defines for the kind (same input types as the full page, through the shared `ent-field` partial), and the **Description** (the Markdown body), open and first-class (4.4A), with a Preview toggle through `/ui/md-preview` and a word count. The Description is saved with the same `base_revision` as everything else on the record form, so a stale save is refused with the conflict box and nothing is lost: **Keep mine** and **Use theirs** apply to the Description exactly as to the summary, and the conflict box shows the saved Description beside the typed one (as the full page's "What is saved now" does). Name and sort name stay on the full page (a Display disclosure), except that the name's other names are edited through "Also known as". Only for `via=primary`; entries reached by reference or link say where they live and link there.
- This book (the override): name, summary, every field (blank means inherit; a "Blank for this book" box where the full page has one), and Private notes for this book. **Clear this book's version** button (`override-clear`; it asks nothing, because nothing is lost that a record edit would not restore: the full page does not ask either).

The form inputs use the full page's names (`f.<key>`, `blank.<key>`, `base_revision`), so one parser in Go serves both: the full page's `uiEntity` and `uiEntityOverride` code is split into `entityUpdateFromForm` and `overrideFromForm`, and both pages call them. **The parser sets only what the form sent.** Today `uiEntity` "save" sets `Name`, `SortName`, `Body` and `Color` unconditionally, so a sidebar form that has no name, sort name or colour would blank them. The shared parser sets each pointer only when the key is present in the form (`r.PostForm.Has`), and a field value only for `f.<key>` keys present. The full page sends everything, so its behaviour is unchanged. Go tests: a sidebar save leaves name, sort name, colour and body untouched; a full-page save still sets them; an absent `f.<key>` leaves that field alone.

**Saving.**
- The form saves by itself, 800 ms after the last input, on change, and when focus leaves a field. A visible **Save now** button exists for no-script reasons of habit and for keyboard users who want certainty; its status line says Saved at HH:MM.
- Transport is HTMX (`hx-post`, `hx-target` is the status line only, with the new revision returned out-of-band as the hidden `base_revision`). The response never replaces a field, so what the writer is typing is never replaced.
- **One save at a time.** HTMX's own queue reads the form when a request is queued, so a second save would carry the old revision and fail against the writer's own first save. `lookup.js` cancels a request while one is in flight (`htmx:beforeRequest`), remembers it, and sends it again with the new revision when the first answers (`htmx:afterRequest`). This is the pad.js rule (one in flight, one more owed).
- **A signed-out session, a stale CSRF token.** Every panel request carries `X-Scriobh-Fetch: 1` (`lookup.js` sets it in `htmx:configRequest`, as `shell.js` sets the CSRF header), so the server answers a lost session with JSON and the real status instead of redirecting to sign-in, which HTMX would follow and swap into the panel. On 401 or 403 the typed words stay in the fields, the status line says "You were signed out. Your words are kept here. Sign in again in another tab, then press Save now", and the next save first asks `/ui/csrf` for a fresh token, exactly as `doc.js` does after a sign-in elsewhere. Go test: a panel POST with an expired session and the fetch header gets 401 JSON, with no sign-in page in the body; with a bad token, 403 and a form that is not replaced. e2e case 18.
- **Revision.** The record form carries `entity.get`'s `revision`. A stale one answers 422 with a conflict box in the status area: "This entry changed since you opened it. Your words are kept." **Keep mine** re-sends with the current revision (a button carrying it); **Use theirs** reloads the entry (`fresh=1`, replacing the form, only on that click). The 409 for a name that another entry holds (aliases) is the same explanation as the full page's, with a button that opens that entry. The override has no revision (as on the full page); the last save wins, and the status line says so.
- **Two tabs.** The second tab's save hits the revision check. No live push in build A (the writing view's stream is project-scoped and entity events carry the compendium). A clean entry re-reads when the browser tab becomes visible again, and an open entry is never replaced while any of its fields is dirty or focused (checked in `lookup.js`, the same rule `compendium.js` uses on the full pages).
- **Leaving the page with a sidebar edit pending.** `doc.js` only knows the editor. `lookup.js` sends the dirty form with `fetch(..., {keepalive: true})` on `pagehide`, and keeps `beforeunload` asking while a save has failed or a conflict is open (never for an ordinary pending debounce, which `pagehide` covers). `doc.js` is not changed.

**The writing surface stays sacred.**
- Typing in the sidebar does not touch the editor: no dispatch, no `focus()`, no `scroll`, no re-read of `markdown()`. The editor's own autosave, lease and draft machinery run exactly as before; entity saves do not change the document's revision.
- No fragment carries `autofocus` unless the writer pressed something (opening an entry, New entry, the chord). A list that loads by itself, a tab restored from the cookie, a status line: none take focus.
- **Return to the editor**: `Esc` anywhere in the panel, or the chord pressed while focus is in the panel, calls `view.focus()` with scrolling prevented. ProseMirror keeps the selection while unfocused, so the caret is where it was. `lookup.js` stops `Esc` there so the inspector's own Escape (close) does not also run. **At 390 px** the panel is a sheet over the editor, so `Esc` also closes the sheet (the editor would otherwise get focus under it, hidden): on a narrow screen `Esc` closes the sheet and focuses the editor, on a wide one it only moves focus. The chord from inside the panel does the same.
- Nothing generated is ever inserted into the text by the panel. (A button that inserts a name at the caret, `[[Name]]` or the name itself, is **not** in this plan: it would be an edit of the manuscript made from a sidebar. If wanted, it is its own row with an explicit accept.)

### 4.6 Permissions: follow the compendium, say why when read-only

The panel computes capabilities per entry, from the same facts the ops check, and the ops stay the authority (a refused write is shown in words, never a raw code).

| Caller | List and entry | Record edit, aliases | This book's version | New entry |
|---|---|---|---|---|
| Not on the compendium | Empty, "not shared with you"; names nothing | no | no | no |
| Compendium reader, project editor | yes | no: "You can read this entry. You are a reader of its Compendium, so only its editors can change it." | yes | no |
| Compendium reader, project reader or reviewer | yes | no | no: "Only editors of this book can set its version." | no |
| Compendium editor or owner, project editor or owner | yes | yes, `via=primary` | yes | yes (primary compendium) |
| Compendium editor, project reader or reviewer | yes | yes (the op allows it; the same person could do it on `/c/`) | no | yes |
| Entry reached by reference or link | yes | no: "This entry lives in <Compendium>. Open it there to change it." (compendium name only because the caller is on it) | yes (project editor) | n/a |
| Compendium in the trash | read | no, "in the trash", ops answer 409 | no | no |
| Compendium archived | yes | yes (archived compendiums stay writable, as the banner says) | yes | yes |

A read-only panel renders the same fields as text (dl), not disabled inputs, with the reason once, above them.

### 4.7 Scene links, and the names in the text

- **Point of view and location from an entry.** The entry view's **Use as point of view** and **Use as location** buttons call `scene.set_pov` and `scene.set_location` with the document's current revision. This is JavaScript (`lookup.js` through `S.write.api`), because the revision is page state HTMX cannot read, and it follows `scene.js`: raise `W.rev` from the reply, dispatch `scriobh:scene` (the Details tab updates), say "Linked." in the live region. A character can only be a point of view, a place only a location (the op checks; the button is shown only for the right kind).
- **"In this scene" hands over.** `scene.js`: the card's link becomes **Open in the Compendium tab** (`S.lookup.open(id)`), the old link to the sheet stays as the second link. About 100 B.
- **Names underlined in the text.** `mentions.js`: Ctrl or Command click and the palette's "Open the entity under the cursor" call `S.lookup.open(id)` instead of navigating (and fall back to the old navigation if `S.lookup` is absent). The card's own link is unchanged. About 100 B. The palette command's label becomes "Show the entry for the name under the cursor".
- There is no separate "link an entity to this scene" action, and none is invented: a scene's entities are what its text names (name or alias), plus point of view and location. Making a word count as a mention is what **Also known as** is for, which is why that action is in build A.

### 4.8 "Also known as" (the request for AKA)

The feature exists (F-116: `entity.alias.add` and `.remove`, detection, alias search); the full page calls it "Aliases", and nobody found it. Build A makes it findable in both places and quick in the sidebar.

**Sidebar.** Directly under the entry's name, label **Also known as**:
- The names as chips (the name, then the optional "where it comes from" note as a title/tooltip text and in the full page). Each has a Remove (compendium editors; same `aria-label`: "Stop calling her by <name>" style, from the catalogue).
- One field, **Add another name**, with Enter to add (`POST lookup/alias`, `entity.alias.add`). Max 200 characters, one line; the field says so only when exceeded.
- **Add the selected words** (button; reads the editor's current selection on click; an empty or multi-line selection says so in the status line and adds nothing).
- On the 409 (another entry uses the word), the status line says the full page's sentence and names the entry, with a button that opens it in the panel. Nothing is added.

**From selected text, "Add as another name for…".**
1. The writer selects "Mrs Riviera" and presses **Alt+Shift+C** (or uses the palette command **Use the selected words in the Compendium**, or the panel's button).
2. The panel opens on `lk-selection`: the words, likely entries (searched by each word, so "Riviera" finds Alexa Riviera), and two actions: **Add as another name for…** and **New entry**.
3. Choosing the first turns the rows into targets (a button **Add as another name** on each). Only entries the caller can edit and that live in the primary compendium are offered; others are listed disabled with the reason. One press adds it (`entity.alias.add`) and opens that entry, which now shows "Mrs Riviera" in Also known as, with "Added." in the status line.
4. **Mentions update.** The alias triggers of migration 1520 only stamp `mention_state`, so the project owes a rescan; the index is rebuilt afterward, and `mention.list` says `indexing` until it is. The client calls `S.mentions.refresh()` (a new export of `ask()` in `mentions.js`, about 40 B, that also resets `retries`), which already waits for typing and unsaved text to settle and polls every 3 s while `indexing` (up to four times). So the new name is underlined a few seconds later, not instantly; the panel says "Added. The name will be underlined in the text in a moment." The e2e waits on `indexing` becoming false, then asserts the mark appears and that the editor's text was not changed.

**Full entity page.** Relabel everything the writer reads:
- `entity.html`: the heading `Aliases` becomes **Also known as**, the lede keeps the collision explanation ("Two entries cannot answer to one word…"), `New alias` becomes **Another name**, `Add the alias` becomes **Add the name**, `No aliases.` becomes **No other names yet.**, `Remove the alias X` becomes **Remove the name X** (aria-label), `Also called:` becomes **Also known as:** in the read view; `project-entity.html` line 18 the same.
- `entityui.go` `entitySaved`: `alias` "Another name added.", `unalias` "Name removed."
- `partials/entity-list.html` line 17 `Also:` becomes `Also known as:`; the search help on `entities.html` ("A name, an alias or a word from its notes") becomes "A name, another name or a word from its notes".
- The catalogue strings for merge and move (`cmg.*alias*`, `compendium.manage.act_drop`) say "alias" in their own sentences. They stay as they are (rare screens; the word there is technical). Noted for the review.
- The id `ea-alias` and the form field name `alias` stay (no script or test needs them renamed except the strings); `e2e-entities.mjs` and `screens-entities.mjs` change their button text and the "Alias added." wait in the same commit.

### 4.9 One chord, and where it lives

**Recommendation: Alt+Shift+C opens or focuses the sidebar's Compendium panel; the toggle to the full page is not built.** Reasons: the owner's goal is not to go to the page; a chord to the page serves the rare case, which a tab, a link and the palette already serve; and the panel chord has a second job (selected text goes to lookup, alias or new entry), which is the whole point of build A.

Behaviour (one press, one meaning, no modes):
- Focus in the editor, no selection: open the inspector on Compendium and focus the search field.
- Focus in the editor, with a selection: open on `lk-selection` for those words, focus on the first action.
- Focus in the panel: return to the editor at the caret (the same as `Esc`).
- Never types a character (`preventDefault` on `keydown`, matched by `e.code === "KeyC"`, like the other chords; ignored while composing and on key repeat).

Letter: C is free (A N P O G S I J B are taken in the three chord tables; the editor bundle's sources bind only Mod-Alt headings and the Mod+Alt+Shift+V paste, no Alt+Shift letter; no `accesskey` in the templates). It is claimed in `S.chords` as `compendium: panel` so the clash check covers it.

Where it lives: **`inspector.js`**, which loads on every writing view after load and already owns `open()`. Cost: about 300 B gzip, so its limit rises from 5,120 to 5,632 (5.5 KB) in the same commit as the `ARCHITECTURE.md` row. If `lookup.js` has not loaded, `inspector.js` sets `S.lookupWant = {sel: text}` and `lookup.js` reads it in `show()` (the `S.labWant` pattern). Not in `doc.js`, `tree.js` or `alts.js`.

It is documented the way every shortcut is after build S: **a registry row** (`compendium_panel`, group Compendium, `Claim: KeyC`) that feeds the shortcuts modal, the palette hint and the toolbar or tab `data-keys`, plus the entry `Alt+Shift+C` in `desktop/src-tauri/src/menu.rs` `APP_CHORDS`. The drift tests of 3A.4 fail the build if the claim in `inspector.js` has no row, or the row has no claim, or the Rust list lacks it. The palette command (+about 250 B in `palette.js`) is added to the registry too. The tab carries `aria-keyshortcuts="Alt+Shift+C"`.

### 4.10 Creating an entry from selected text

- Entry points: the chord with a selection, the palette's **New entry from the selected words**, the **New entry** button of the list (name empty).
- `lk-new`: Name (prefilled from the selection, trimmed to one line, 200 characters), Kind (radios: Character, Place, Item, Organisation, Concept; no default, so a wrong kind is never silent), Summary (optional, one line). Nothing else: custom fields, body, relationships are added afterward in the entry view that opens.
- `POST lookup/new` calls `entity.create` on the project's **primary** compendium. A name collision (409) shows the existing entry with an **Open it** button and keeps the writer's words. A project with several compendiums still creates in the primary; the reason is stated once.
- Success opens the new entry (heading focused, because the writer pressed Create), "Added to <Compendium>." in the status line.
- The mention index updates by trigger; the marks follow through `S.mentions.refresh()`.
- Not offered when the project has no primary (the empty state's **Make one for this book** is the way), when the caller is not an editor of the primary, or when the compendium is in the trash.

### 4.11 Files

New:
- `internal/server/lookupui.go` (handlers, view building, capability rules).
- `web/templates/partials/lookup.html` (fragments).
- `web/static/js/lookup.js`.
- `web/static/css/lookup.css` (lazy; loaded by `lookup.js` before the first fragment, like `review.css`): the panel's own layout only.
- `web/templates/partials/entry.html` and `web/static/css/entry.css`: the shared entry component of 4.4A (grid, quiet slots, counted sections, compact list row). Loaded by the entity pages through their `head` and lazily by `lookup.js`.
- `docs/screens/entities-before/` and `docs/screens/entities-after/` (the acceptance screenshots).
- `internal/server/lookupui_test.go`, `internal/server/authz_sidebar_test.go`.
- `scripts/e2e-sidebar.mjs`.

Changed:
- `web/templates/write.html` (tab, panel, `data-compendium-src`, `data-compendium-css`). **Merge note:** `split-chapter` also edits the long `#write` attribute line and the Insert group; take its version and add the two attributes.
- `web/static/js/inspector.js` (panels, `LAZY`, chord, claim).
- `web/static/js/scene.js`, `web/static/js/mentions.js` (4.7, `S.mentions.refresh`).
- `web/static/js/palette.js` (commands and hints; `split-chapter` already rewrote the split command there).
- `web/static/css/inspector.css` (tab strip overflow).
- `internal/server/write.go` (cookie whitelist).
- `internal/server/entityui.go`, `internal/server/entityproj.go` (shared form parsers and field rows; the view struct the shared `entry.html` reads; the section counts).
- `web/templates/entity.html` (rebuilt on `entry.html`: header, grid, Description, counted sections, section index; the Details form keeps its action, field names and ids), `web/templates/project-entity.html` (the book's sheet, same component), `web/templates/entities.html`, `web/templates/partials/entity-list.html` (compact rows for both lists), `web/static/css/entities.css` (the sparse rules removed: `.facts` cards, section margins, stacked fields), `web/static/js/entity.js` (the Preview button keeps working in the new markup; limit 2,048 unchanged), `scripts/screens-entities.mjs` (named before and after shots).
- `web/templates/entity.html`, `project-entity.html`, `partials/entity-list.html`, `entities.html` (4.8).
- `internal/i18n/locales/en.json`, `internal/i18n/i18n_test.go` (`"lookup.js": "lk.js."` in the prefix table; `split-chapter` edits the same line, so merge by hand).
- `internal/server/web_test.go` (`lazyJS`: `js/lookup.js` 4096; `js/inspector.js` 5632).
- `docs/FEATURES.md`, `docs/ARCHITECTURE.md` (the lazy-files row), `docs/ACCESSIBILITY.md`, `docs/OPEN-QUESTIONS.md` (decisions below), `Makefile` (`e2e-fast` gets `e2e-sidebar.mjs`).

### 4.12 Ops, and why none are new

Every action maps to an existing op (list in section 2). The one place an op might be short: `project.entity.get` does not return the record's `revision`, so the entry view makes a second call, `entity.get` (compendium reader), for the revision, aliases with ids and relations count. That costs one more read and no new surface. If the review prefers one call, the fix is to add `revision` to `ProjectEntity`, which is a wire-shape change and then does need `make skill`, the sweep and the snapshot; it is not needed.

### 4.13 i18n keys (prefix `lk.` for templates and Go, `lk.js.` for `lookup.js`)

`lk.tab` Compendium; `lk.search` Find in the Compendium; `lk.kind_all`; `lk.more`; `lk.hidden` Entries in a Compendium you are not on are left out; `lk.none` / `lk.none_owner` / `lk.none_other` / `lk.make` (empty state); `lk.closed` (not shared); `lk.back` Back to results; `lk.aka` Also known as; `lk.aka_add` Add another name; `lk.aka_none` No other names yet; `lk.aka_remove` Remove the name {name}; `lk.aka_selected` Add the selected words; `lk.aka_collision` the 409 sentence with `{name}`; `lk.aka_added`; `lk.aka_removed`; `lk.sel_h` The selected words; `lk.sel_already` Already a name of {name}; `lk.sel_alias` Add as another name for…; `lk.sel_new` New entry; `lk.sel_target` Add as another name; `lk.sel_cant` (target not editable, with reason); `lk.scope_book` This book; `lk.scope_all` Everywhere; `lk.scope_book_help` / `lk.scope_all_help`; `lk.ro_compendium` / `lk.ro_book` / `lk.ro_ref` / `lk.ro_trash` (the four reasons in 4.6); `lk.via_primary` / `lk.via_reference` / `lk.via_link` / `lk.via_other` (fallback tag, for project-local); `lk.book_version`; `lk.clear_book` Clear this book's version; `lk.desc` Description; `lk.notes` Notes for this book; `lk.blank_book`; `lk.saved` Saved at {time}; `lk.saving`; `lk.fail`; `lk.conflict` + `lk.keep_mine` + `lk.use_theirs`; `lk.book_lww` The last save wins for this book's version; `lk.new_h` New entry; `lk.new_name` / `lk.new_kind` / `lk.new_summary` / `lk.new_create`; `lk.new_added` Added to {name}; `lk.new_collision`; `lk.use_pov` / `lk.use_loc`; `lk.linked` Linked.; `lk.open_comp` Open in the Compendium; `lk.open_sheet` This book's sheet; `lk.open_where` Where this appears; `lk.js.sel_empty` Select the words in the text first.; `lk.js.sel_long` That selection is too long or runs over a line.; `lk.js.offline` Could not reach the server. Your words are kept here.; `lk.js.signed_out`; `lk.js.loading`; `lk.js.pov_set` / `lk.js.loc_set` Linked.; `lk.js.pov_fail`; `lk.js.saved` Saved at {time}; `lk.js.saving`; `lk.js.conflict_open`; `lk.js.stale_csrf`; `lk.js.alias_added` (at least ten, because `TestEveryKeyTheScreensUseIsInTheCatalogue` fails a listed script with fewer than ten `T("key", "default")` messages, and `split-chapter` had to exempt `split.js`; the plan registers `"lookup.js": "lk.js."` in the prefix table and writes every message in the script through `T`, so no exemption is needed); plus `entity.aka.*` for the relabelled full page. `scene.js` and `mentions.js` keep their English defaults (their files have none in the catalogue today).

### 4.14 Budget numbers for A

| Item | Now | After (estimate, to be measured) | Limit |
|---|---|---|---|
| `doc.js` + `tree.js` | about 16 B free after `split-chapter` | **unchanged** | 22,016 |
| `inspector.js` | 5,000 | about 5,300 | 5,120, raised to 5,632 in the same commit as the doc row |
| `lookup.js` (new, lazy, first open) | n/a | about 3,400 | 4,096 |
| `lookup.css` (new, lazy, first open) | n/a | about 1,600 | not in the per-page count (loaded after load, as `review.css`); sanity cap 3 KB |
| `scene.js` | 3,622 | about 3,720 | 4,096 |
| `mentions.js` | 4,493 | about 4,640 | 5,120 |
| `palette.js` | 7,216 now; about 7,900 after build S | about 8,150 | 12,288 |
| shell JS total | 31,547 now; about 32,250 after S | about 32,500 | 40,960 |
| writing page CSS on load | 22,332 now; about 22,730 after S | about 22,830 (tab strip rule) | about 25,600; hard stop at 24,000 |
| `entry.css` (new) | n/a | about 1,500 gzip; loaded by the entity pages (their total CSS 12,710 now, about 13,400 after, with the sparse rules removed from `entities.css`, of 25,600) and lazily by the sidebar | per-page 25,600 |
| Entity page height for the reference entity (nine fields, three names, two relationships, 200 words) at 1280 | measured before | at most 60 percent of before | acceptance in 4.4A |
| Entity list rows on screen at 1280 by 800 / 390 by 844 | about 6 / about 4 | at least 15 / at least 8 | acceptance in 4.4A |
| Server: list fragment at 5,000 entities | n/a | p95 at most 150 ms (the same op as the full page's list) | 150 ms |
| Server: entry fragment | n/a | p95 at most 50 ms | 50 ms |
| Save to status line, same machine | n/a | p95 at most 100 ms | 400 ms (the document save budget) |

The JS figures are estimates from similar files; build A measures and writes the real ones into `ARCHITECTURE.md`. The CSS rule: the panel's styles are a lazy sheet so the writing page's initial CSS is almost unchanged.

### 4.15 Tests for A

**Go handler tests (`lookupui_test.go`).**
- List: primary, referenced and linked entities appear with the right `via`; the book's override shows its name and the tag; query by name, by alias, by word in the selection; paging; kind filter; at 5,000 entities the first page and a search stay within the budget above.
- Entry: record view carries `revision`; scope switch; the body is rendered through the whitelist renderer (raw HTML shown as text).
- Save: success returns the new revision out-of-band and changes only the status line; stale revision gives 422 with both texts' conflict box and changes nothing; the typed values are not in any replaced element.
- Override set, clear, blank field, notes; the Compendium's own record is unchanged after an override (the existing full-page guarantee, asserted again through the panel).
- Alias add, remove, the 409 naming the holder, a 201-line name, a name with a newline; selection list ranks "Riviera" to Alexa Riviera.
- New entry: kind required, collision shows the existing entry, success creates in the primary and not in a reference; not offered to a compendium reader; not offered with no primary.
- `make` (compendium for the book): owner only.
- State matrix of 4.6 as a table test, one row per line.
- The full entity page after the relabel: the same operations through `/ui/entities/{id}/alias` and `unalias` still work, with the new words.

**Authz and leakage (`authz_sidebar_test.go`; run with `go test ./internal/server -run Authz`).**
- A project editor who is **not** on the compendium: the list and the entry fragments contain no entity name, no summary, no compendium name, no count; a `GET lookup?entity=<id>` and every `POST lookup/*` with a real entity id of that compendium answer exactly what they answer for an id that does not exist.
- An entity id from a compendium of another book, or one the caller is on but this project does not reach: 404, same as unknown.
- A compendium **reader** who is a project editor: every write route refuses (the entity ops answer 403 or 404 as they do through REST) and the fragment shows the reason and no inputs.
- A project **reader** or reviewer on the book: override and new are refused; reading follows the compendium.
- A locked project (passphrase, not unlocked): `TestGatePageSweep` walks the new routes automatically (they sit under `/ui/projects/{id}/`); the gate world's canaries ("Zqoverride", the entity's name) must not appear. A test adds the panel's list and entry for the gate world's entity.
- A trashed project and a trashed compendium: 404 and read-only with the reason.
- A key pinned to another project or compendium: the routes are session routes, so the ops refuse as they do now (run `TestKeySweep` unchanged).
- **A project title never leaks**: the panel names no project except the one it is on. The compendium's name appears only when the caller is on it. (The Back link of build C is the place a project title could leak; its tests are in section 6.)

**Existing tests that must stay green:** `go vet ./...`, `go test ./...`, `go test ./internal/server -run Authz`, `go test ./internal/editor -run RoundTrip`, `TestWritingViewBudgets`, `TestCSSBudgetPerPage`, `TestAssetBudgets`, `TestEveryRouteIsClassifiedForTheGate`, `TestGatePageSweep`, the i18n catalogue test, `scripts/check-contrast.py` (the new sheet uses tokens only), `scripts/csp-smoke.mjs` (no inline script or style: the fragments use classes only).

**End-to-end (`scripts/e2e-sidebar.mjs`, added to `e2e-fast`).** Each case runs on one page that stays open, so the editor's state can be compared:
1. **The editor is untouched while the sidebar is edited.** Open a 3,000-word chapter, scroll to the middle, put the caret in a paragraph and type "XYZ" without waiting for the autosave. Snapshot `window.scrollY`, `scriobh.write.markdown()`, the selection anchor and head, `scriobh.write.rev` and `scriobh.write.unsaved()`. Open the tab (a click, then by the chord), search, open an entry, type in Summary, a field and Notes, add a name, wait for Saved. Assert every snapshot value is identical, the editor never had focus during sidebar typing, and the editor box (paper rect) never moved (sampled with a `ResizeObserver` and a `requestAnimationFrame` loop). Then `Esc`: focus is in the editor, the caret is where it was, scroll is unchanged, and typing "Q" lands at the caret. The unsaved "XYZ" then saves by itself with the right content and revision.
2. **Lookup.** Search by name, alias, word; the entry shows fields, also known as, the body; both links open the right pages (after the editor's text is flushed: type, click the link, reload shows the text saved).
3. **Edit with two tabs.** Tab 2 changes the entry through the API; tab 1's next sidebar save shows the conflict box, keeps its typed words, **Keep mine** then wins, a second edit on **Use theirs** replaces the form only on that click. A dirty form is not replaced when the tab becomes visible.
4. **This book's version.** Set "age" for the book; the Compendium's record shows the old age on `/c/…`; clear it.
5. **Also known as, typed.** Add "Mrs Riviera" to Alexa Riviera with Enter; the collision for a word another entry holds names that entry; remove one.
6. **Also known as, from selected text.** Type "Mrs Riviera said hello." in the chapter, select the two words, press Alt+Shift+C, choose **Add as another name for…**, pick Alexa Riviera. The panel shows the name, and the mark under "Mrs Riviera" appears in the text; the text itself is unchanged.
7. **New entry from selected text.** Select "Captain Vell", chord, **New entry**, kind Character, Create. The entry opens, and the name is underlined in the text. A name that exists shows the existing entry instead.
8. **Scene links.** On an entry that is a place, **Use as location** sets it (the Details tab and the scene tab show it, `W.rev` rose); the scene tab's card **Open in the Compendium tab** opens the entry; Ctrl-click on an underlined name opens the entry in the panel and does not navigate.
9. **Read-only.** A compendium reader who is a project editor: no inputs, the reason shown, "This book's version" still works if they are a project editor; a project member who is not on the compendium: the "not shared with you" state with no names; no-compendium state and the owner's **Make one for this book**.
10. **Stays open across chapters.** On a wide screen, open the tab, move to another chapter with the keyboard (Alt+PageDown): the panel is drawn open, restores the last entry, and the first fragment arrives without the editor moving.
11. **390 px.** The inspector is a sheet: the tab strip scrolls to Compendium, the search field is 16 px or larger and 44 px high, rows and buttons are 44 px targets, the sheet stays above the emulated keyboard class (`html.kb`), the sheet's content scrolls inside it, nothing overflows horizontally, and `Esc` or the close button returns to the editor.
12. **Keyboard only and themes.** Reach every control with Tab and the arrow keys (tab strip, search, rows, Enter opens, Esc returns); visible focus; screenshots in Paper and Ink compared in `scripts/check-contrast.py`'s token set; reduced motion.
13. **Offline.** After a reload with the network cut the panel says the Compendium could not be reached, keeps the editor working, and does not lose a typed sidebar edit (it stays in the field and retries; `beforeunload` asks).
18. **Signed out while the sidebar is open.** Revoke the session from another context, then edit a field: no sign-in page appears in the panel, the typed words stay, the status line says so, and after signing in again elsewhere **Save now** saves with a fresh token; the editor's own text is still unsaved-safe (its draft is kept, as today).
19. **The description is editable and protected.** Type in the Description of an entry (a long paragraph), wait for Saved; edit the same entry's Description through the API in between: the next sidebar save shows the conflict box with both texts, the typed paragraph stays in the field, **Keep mine** and **Use theirs** behave as for the summary; the Preview toggle renders the text through the whitelist (raw HTML shown as text); the editor snapshot of case 1 is unchanged throughout.
20. **Density.** The numbers of 4.4A (page height at most 60 percent of before; every defined field visible as a value or a quiet slot; "Show N more fields" with the right N; at least 15 list rows at 1280 by 800 and 8 at 390 by 844); a quiet slot filled in the sidebar saves as a field; the section index links reach their sections with the keyboard; sections open and close with Enter and Space and remember their state per tab; both themes and 390 px screenshots stored as the "after" set, with no horizontal overflow.

**Existing e2e scripts that must stay green:** `e2e-entities` (updated for "Also known as" and the Description label; the Save button, the ids and the 409 explanation are kept), `e2e-phase3` (the 5,000-entity list at its budget, with the compact rows), `e2e-shortcuts` (build S), `e2e-write`, `e2e-write2`, `e2e-phase2`, `e2e-long` (the 20,000-word chapter), `e2e-alts`, `e2e-alts2`, `e2e-ai-edit` (the chord table must report no clash), `e2e-ai-lab`, `e2e-scene`, `e2e-mentions`, `e2e-entities` (updated for the relabel), `e2e-phase3`, `e2e-compendium`, `e2e-compmanage`, `e2e-live`, `e2e-gate`, `e2e-a11y` (both themes, 1280 and 390, keyboard), `e2e-i18n`, `e2e-pwa` (offline shell: every static file is already precached, so the lazy files need no list), `e2e-split` (after it merges), `e2e-library`, `e2e-desktop`, `scripts/desktop-smoke.sh`, `scripts/csp-smoke.mjs`.

**Desktop smoke.** All links are relative, no URL is read from the request, the routes sit on the same origin the page came from, nothing depends on a proxy or a fixed port. The existing desktop smoke (ephemeral loopback port, no external IdP) is run with the tab opened and an entry edited.

**Docs.** `FEATURES.md` rows below; `ARCHITECTURE.md` lazy-files row (the numbers above, measured); `ACCESSIBILITY.md` writing-view line (the chord, Esc, the tab); `OPEN-QUESTIONS.md` decisions: tab not scene extension, one save at a time, no live push in A, project-local not shown, create in primary only, Description editable in the sidebar.

### 4.16 FEATURES rows for A

| Row | Text |
|---|---|
| F-1253 | The inspector's **Compendium** tab: search the entries the person may read (names, also-known-as names, words in a summary), the book's version tag, referenced and linked entries, paging, the empty states (no Compendium, not shared with you), kept open from chapter to chapter and restored to the last entry in the tab. |
| F-1254 | The entry in the sidebar: summary, fields, description, edited in place with autosave, one save at a time, the revision check and its Keep mine and Use theirs, nothing in the editor moving or losing focus, Esc back to the caret. |
| F-1255 | This book's version in the sidebar: name, summary, fields and notes for this book only, clear it, read-only reasons that follow Compendium membership (F-476). |
| F-1256 | **Also known as**: a name's other names right under it in the sidebar, quick add and remove; the full page's "Aliases" relabelled; the collision explanation kept (F-116). |
| F-1257 | Selected text to the Compendium: **Add as another name for…** and **New entry** from the editor's selection, with likely entries by word; the marks in the text update for the new name. |
| F-1258 | Scene links from the sidebar: use an entry as point of view or location, the scene tab and an underlined name (Ctrl or Command click, the palette) open the entry in the tab instead of leaving the page. |
| F-1259 | **Alt+Shift+C** opens or focuses the Compendium panel (with the selected words), and returns to the editor from it; claimed in the chord table; in the palette and the tab's `aria-keyshortcuts`. |
| F-1260 | Make a Compendium for a book from the sidebar (the owner), and the read-only panel for a person who is not on the Compendium. |
| F-1261 | **A denser Compendium.** One entry component for the sidebar and the full entity page: a compact header (name, kind, also-known-as chips, summary), fields in a two-column grid with empty fields as quiet slots and a visible count of more fields, the Description open and editable, collapsible sections with counts, a section index on the page, inline editing with no edit mode; compact one-line rows in the entity lists. Before and after screenshots and measured thresholds. |

## 5. Build B: events this scene tells, and a quick note

Smaller, same panel, its own review, after A is in use.

**Events this scene tells (F-121 on the scene).** A sub-view **This scene** of the Compendium tab:
- The events this document tells. Read with the existing `timeline.event.list` on the primary compendium with `project=<this project>`; each event carries `scenes[]` (`SceneLink.ID` is the document id), and the handler keeps the events whose scenes include this document. No new op. Cost: the project's events for one compendium, a few hundred at most; measured at build against 5,000 events, p95 at most 100 ms.
- **Unlink** (`timeline.scene.unlink`), **Link an event** (search `timeline.event.list` by `entity` or title words on the compendium, then `timeline.scene.link`), **New event** with title and story date only (`timeline.event.create`, compendium editor; date hints, participants, place and narrative order stay on the full page). A link to the event's full page for the rest.
- Permissions as 4.6: link and unlink need project editor; create needs compendium editor; a reader sees the list as text with the reason.

**Quick research note.** A sub-view **Notes**:
- The book's ten most recently changed notes (`project.research.list`), search, open one read-only (rendered), **New note** with title and body (`project.research.create`, project editor; saved with the revision rule of 4.5), **Open all notes** to the page. No tree, board, tags, moves or clippings.

**Routes.** The same two patterns as A: `GET /ui/projects/{id}/lookup?view=events|notes` and `POST /ui/projects/{id}/lookup/{action}` with actions `event-link`, `event-unlink`, `event-new`, `note-new`, `note-save`.

**Files.** Changed only: `internal/server/lookupui.go`, `web/templates/partials/lookup.html`, `web/static/js/lookup.js`, `web/static/css/lookup.css`, `internal/i18n/locales/en.json`, `internal/server/web_test.go` (`js/lookup.js` 4,096 to 5,120), `docs/FEATURES.md`, `docs/ARCHITECTURE.md`, `Makefile` (no change: same script). New tests: `internal/server/lookupui_events_test.go`, `internal/server/authz_sidebar_events_test.go`.

**i18n keys.** `lk.view_scene` This scene; `lk.view_notes` Notes; `lk.ev_h` Events this scene tells; `lk.ev_none` None yet; `lk.ev_link` Link an event; `lk.ev_unlink` Stop telling {title}; `lk.ev_new` New event; `lk.ev_title` / `lk.ev_date` / `lk.ev_create`; `lk.ev_open` Open the event; `lk.ev_ro`; `lk.nt_h` Recent notes; `lk.nt_none`; `lk.nt_new` New note; `lk.nt_title` / `lk.nt_body` / `lk.nt_create`; `lk.nt_all` Open all notes; `lk.nt_ro`; and `lk.js.*` for any script message (at least the autosave status words already in A).

**Budget.** `lookup.js` about 4,600 of a raised 5,120 (the two sub-views reuse A's saver); `lookup.css` +0.4 KB; palette +100 B; nothing else.

**Tests.**
- Go: both sub-views with the A matrix: an event of a compendium the caller is not on is never named, and linking it answers as for an unknown id; a note of a project the caller cannot open; a reader gets text and a reason; a stale note revision gives the conflict box and keeps the words; the events list matches the full timeline page for the same scene.
- Authz: the new actions go through `timeline.scene.link`, `timeline.event.create` and `project.research.create`, which are already in the sweep; the new test asserts the panel never shows an event title or note title to a person the ops would refuse; a locked project through `TestGatePageSweep`.
- e2e (`e2e-sidebar.mjs`, cases 14 and 15): an event is linked and unlinked and a note created and edited with the editor's text, caret, scroll, revision and unsaved state unchanged (the case-1 snapshot); the 390 px sheet; keyboard only. `e2e-timeline`, `e2e-research`, `e2e-phase3` and the build A scripts stay green.

**Rows.** F-1262 (events this scene tells, from the sidebar), F-1263 (quick research note from the sidebar).

## 6. Build C: the rare trip out

Small fallbacks, built last. Everything the writer does often is already in the sidebar.

### 6.1 "Back to writing" on compendium pages

- A slim line at the top of `<main>` on every `/c/…` full page: **← Back to *Title*, <document title>** going straight to the document, plus the chord hint nowhere (there is no chord here). It is in `<main>`, before the banner, not in the sticky header, because the header at 390 px already gives way (ellipsis) and the crumb partial is called with a dictionary from twelve templates; one layout change instead of twelve.
- Rule, with no new write and no new op: the project the caller was most recently in (`project.list`: `last_opened_at`, `last_document_id`, members only) among the projects that use this compendium and the caller can open (`compendium.get`'s `projects`), skipping trashed ones; its last document (`doc.get`, by title only: one cheap read; no `doc.list`, no chapter number). Computed in `page()` next to `compBannerFor`, which already lists compendiums for the banner, so the extra work is `compendium.get`, `project.list` and one `doc.get`.
- A locked project (passphrase, this session has not unlocked): `project.list` redacts it to a title; the line says **Back to *Title*** and goes to `/p/<address>` (the unlock page). No document title, no chapter text.
- A person who is not a member of the project, or whom the compendium does not list it for, gets no line at all. The link is built from server data only; nothing is read from the request.
- Tests: Go handler tests (the right project among two that share the compendium; none when the caller has not opened any; a trashed project skipped; the locked project's title only; the label for an untitled document); authz and leakage: a compendium member who is not on the project sees no line and no title (404-equivalent: nothing), a project title appears only to members of that project, `TestGateCompendiumPagesAreSwept` (the canary document title "Zqtitle" and the subtitle must not appear for the editor and owner2 who have not unlocked; the title "Gate Book" may); cost: at most 10 ms p95 added on the entity list at 5,000 entities with 50 projects. `e2e-sidebar.mjs` case 16 (the link goes to the document; the chapter's text is saved and shown), at 390 px and keyboard only.

### 6.2 The Compendium tab goes to the compendium; the wiring page stays

- The project strip's Compendium tab and the phone's **Bible** tab point to `/p/<slug>/compendium/open` (one new GET route under the project address, so the gate covers it). It redirects (303) to `/c/<address>` when the project has a primary the caller is on, and otherwise to `/p/<slug>/compendium` (the wiring page, which offers to create or attach one). The redirect is relative.
- The wiring page keeps its address (existing tests and links use it). It is reachable from a new **Compendium settings** item in the project menu (`project-more`) and from a **Settings** link beside each project in the compendium overview's "Projects that use it".
- The palette's "Go to the Compendium of this project" points to the same open route.
- Tests: handler tests (primary and member: redirect to `/c/…`; no primary: the wiring page; primary but not a member: the wiring page, which says why; reader), `e2e-compendium`, `e2e-compmanage`, `e2e-a11y` stay green (they visit `/p/<slug>/compendium` directly); a new case in `e2e-sidebar.mjs` for the tab.

### 6.3 Dropped: the toggle chord and `?from=<project>`

- **Toggle chord:** not built (4.9). The Back link, the tab and the palette are enough for the rare trip; the budget goes to Alt+Shift+C.
- **`?from=<project slug>`:** evaluated and **not** built. It fixes two books sharing a compendium in two tabs only for the first compendium page, because the tab strip and the links inside the compendium drop it, and carrying it through means about thirty template calls or a click handler that rewrites links. With almost all compendium work in the sidebar, which is per book and so never ambiguous, the trip out is rare and the last-opened rule is right in nearly every case. If two-tab use turns out to matter, the clean fix is a per-tab client record of the project being written, not a query parameter.

### 6.4 Caret and scroll come back

- New `web/static/js/resume.js` (lazy, in `data-ext` with the other writing-view parts). On `pagehide` it keeps, in `sessionStorage` (per tab, no server state): the document's revision, the editor selection (anchor and head) and `scrollY`, under `scriobh:pos:<person>:<doc>`.
- On the next load of the same document in the same tab, by Back, reload or the Back link (the link adds the fragment `#resume`; the script removes it with `replaceState`): if the revision is unchanged, restore the selection (without scrolling) then `scrollTo(0, y)`, then scroll the caret into view only if it is off screen; if the revision changed, restore scroll only. Never when the page has `?find=` or `?new=1`, in Source mode, or after the writer has already typed, clicked or scrolled. On a fine pointer with `#resume` the editor takes focus; otherwise it never takes focus by itself (a coarse pointer would raise the keyboard).
- Budget: about 0.7 KB gzip, `lazyJS` limit 1,024. `doc.js` and `tree.js` untouched.
- Tests: `e2e-sidebar.mjs` case 17 (long chapter, caret and scroll restored after the Back link and after a reload; not restored when the text changed in between), `e2e-write`, `e2e-long`, `e2e-split` stay green (the split's `pageshow` reload must not fight it: the script does nothing when `?new=1` is present).

### 6.5 FEATURES rows for C

| Row | Text |
|---|---|
| F-1264 | **Back to writing** on every Compendium page: the book most recently written that uses this Compendium, and its last document; a locked book by title only; nothing for a person who cannot open the book. |
| F-1265 | The project's Compendium tab opens the Compendium (`/p/{slug}/compendium/open`); the wiring page stays at `/p/{slug}/compendium`, from the project menu and the Compendium overview. |
| F-1266 | The writing view puts the caret and scroll back after a Back-button, reload or **Back to writing** return, per tab, when the text has not changed. |

### 6.6 Files and i18n for C

**Files.** New: `web/static/js/resume.js`. Changed: `internal/server/compui.go` (`compNav`: banner and Back from one `compendium.list`), `internal/server/render.go` (`Page.Back`), `web/templates/layout.html` (the line in `<main>`), `web/static/css/app.css` (the line's style; loaded everywhere, about 150 B), `internal/server/projectcompendium.go` (the open route), `web/templates/partials/ptabs.html` (both tab links, and the **Compendium settings** menu item in `project-more`), `web/templates/compendium.html` (**Settings** beside each project), `web/static/js/palette.js` (the command's address), `web/templates/write.html` (`resume.js` in `data-ext`; `split-chapter` edits that line too), `internal/server/web_test.go` (`js/resume.js` 1,024), docs. Tests: `internal/server/compback_test.go`, `internal/server/authz_compback_test.go`.

**i18n keys.** `nav.back_to_book` Back to {project}, {document}; `nav.back_to_book_locked` Back to {project}; `nav.untitled` Untitled; `nav.comp_settings` Compendium settings; `nav.comp_settings_row` Settings. (`resume.js` shows no words.)

**Budget summary for C.** `resume.js` about 0.7 KB of 1,024 (lazy); `app.css` +150 B on every page (the base sheet; every page stays far under 25 KB: the largest is the writing page at about 22.4 KB, which loads `app.css` as well, so it rises by the same 150 B); `palette.js` +0 (an address changes); `doc.js` and `tree.js` untouched.

## 7. Order, reviews, and what to watch

1. **The undo and redo build** (not in this plan) goes first.
2. `split-chapter` merges (it moves again: it is at bbdcb9ee now, and its newest commit lists Alt+Shift+B in the desktop chord list). Rebase; re-run `TestWritingViewBudgets` for the real headroom of `doc.js` and `tree.js`.
3. **Build S**, own review: the registry, the modal, the drift tests. It reads the key rows of the undo and redo build; if that build adds a key, S documents it.
4. **Build A**, one review: the sidebar, the density work and the relabel. Take the "before" screenshots first (4.4A). Run `e2e-sidebar.mjs` first; case 1 (the editor untouched) is the acceptance test, case 20 the density numbers.
5. **Build B**, own review.
6. **Build C**, own review.

Risks and things not verified from reading the code alone:
- HTMX focus after a swap (`autofocus` handling in htmx 2.0.11) and `view.focus()` not scrolling: both checked in the e2e, not assumed.
- Whether the page scrolls in the window or an inner element on every width: the plan assumes the window (`write.css`), the e2e records both.
- `keepalive` fetch on `pagehide` in Safari; the fallback is `beforeunload` asking while a save is pending.
- The inspector's tab strip at 390 px with nine tabs.
- `field-sizing: content` is not in every browser; the fallback is a fixed row count that grows by a few lines of script only if the e2e shows it is needed.
- The shortcuts scan finds claims by pattern; a key handler written in a new style would escape it. The review lists any it cannot see, and the registry's per-scope rows are the second check.
- JS and CSS sizes are estimates until measured.

## 8. Decisions made, and what is left

**Decided by the owner.**
1. The long Description is editable in the sidebar, as a first-class part of the panel (4.4A, 4.5), with the stale-revision protection kept. The owner's reason: the sidebar should be the main place to interact with the Compendium.
2. **No button that inserts an entry's name (or a link) at the caret.** The sidebar never writes into the manuscript; the only text that enters the chapter is what the writer types in the editor. Recorded as a decision so it is not re-opened by accident (and so `OPEN-QUESTIONS.md` gets a line).

**Left, none blocking.**
- The full entity page keeps an explicit Save (sticky) while the sidebar autosaves (4.4A). If you want autosave on the full page as well, say so; it is the same saver and a small follow-up.
- `?` does not work inside the editor (it types a question mark). The way in from the editor is the palette or the header icon. If you want a chord for it too, Alt+Shift+K is free; I have not added it.
