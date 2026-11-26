# Fixes from the November usability pass

Source: [research/UX-PASS-2026-11.md](../research/UX-PASS-2026-11.md). This plan turns that report's verified bugs and recommendations into batches. Each batch is built on its own branch, reviewed, merged and pushed before the next starts. The file and line references were taken on main at bb2f0cf7; check them before editing, because lines move.

## Owner decisions (2026-11-24)

- **Selection bar: yes.** A small bar appears over a selection with Add to Compendium, Look up and Comment. It appears only when a selection is made, never while typing, and the same items go in the right-click menu.
- **First names: offer, don't match.** An entry suggests its first name as an alias (one click). Nothing is matched automatically that the writer did not type.
- **Versions: also before big cuts.** Keep the 5-minute rule, and also keep a version when a save removes more than 20% of the chapter or 200 words, and on Split, Join and an import into a project.
- **"This scene" becomes "Timeline"** in the sidebar's Compendium tab.

## Rules for every batch

- **Read first.** Read AGENTS.md first, especially invariant 3 (one op registry), 8 (nothing animates, reflows or steals focus while typing; live updates never clobber unsaved input) and 10 (budgets).
- **Branch.** Work on a branch `ux-<batch>` in a worktree: `git worktree add /private/tmp/scriobh-ux<batch> -b ux-<batch> main`, run from the repository root.
- **Commits.** Commit in steps. No push, no AI mentions, no Co-Authored-By or attribution lines.
- **Off limits.** Never read `/Users/crueber/dev/safekeeping/scriobh` or `archive/writefreely-scaffold`.
- **Environment.**
  - PATH is exactly `/Users/crueber/.local/share/mise/installs/go/1.27.4/bin:/Users/crueber/.local/share/mise/installs/node/latest/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin`. The inherited PATH is corrupt and breaks node's child processes.
  - Set `GOMAXPROCS=2` and use `go test -p 2`.
  - If the worktree has no `scripts/node_modules`, symlink the main checkout's, and run scripts with `node scripts/<name>.mjs` directly, not through make.
  - Kill only your own processes, and leave no background shells.
- **Every new string goes in the catalogue** (`internal/i18n`, `en.json`) with its English default where the pattern needs one.
- **Writes go through ops.** If an op changes, run `make skill`.
- **Budgets.** Check every touched JS or CSS file against its limit in `internal/server/web_test.go` (`TestWritingViewBudgets` and the lazy-file table), and give before and after numbers. `doc.js` + `tree.js` have about 16 B of room, so batch U2 makes room first. Never raise a limit without writing why in docs/ARCHITECTURE.md in the same commit.
- **Docs.** Each change gets a line in docs/FEATURES.md (the next free F-id after the highest on main; check with `grep -o 'F-1[0-9]*' docs/FEATURES.md | sort -t- -k2 -n | tail -1`) and a note in OPEN-QUESTIONS.md for anything decided along the way.
- **Failures.** Never call a failure flaky. Find the assumption.
- **Tests on macOS.** In Playwright on macOS, Home and End go to the start and end of the whole document, not the line. Use Meta+ArrowLeft/Right for the line, or a mouse drag or triple-click.
- **Gates in every batch.**
  - `go vet ./...`, `make fmt-check skill-check`, `go test ./internal/repocheck`.
  - `SCRIOBH_TIMING=off go test -p 2` on the packages touched, plus `./internal/server ./internal/i18n`.
  - The batch's own e2e script, the existing e2e scripts that cover what changed (named in each batch), `scripts/csp-smoke.mjs`, and `scripts/e2e-a11y.mjs --only <family>` for touched pages.
  - Real Chromium screenshots of anything visible, light and dark, 1280 and 390 px. Look at them.
- **Report.** Commits, before and after budget numbers, what was seen, anything unverified.

## U0. Is a Word table's text kept? (check only, no code)

Make a .docx with a heading, a paragraph, a 3×4 table with words in every cell, then another heading and paragraph. Use python-docx if it is installed, or LibreOffice from an HTML file with a `<table>`. Import it through the import screen. Report:
- the review row's word count and its first line;
- whether the table's words appear in the imported chapter, and in what form.

Also check what `internal/imports/docxconv.go` does with `w:tbl`. If table text is dropped, say so plainly; the fix becomes batch U1b. Report only.

## U1. The import review (before the release)

1. **Enter submits the form twice.** The row form in `web/templates/partials/import-review.html` (~line 96) is `method=post action=…/update` with `hx-trigger="change"`. Enter sends both the change event (htmx) and the browser's own submit. The page lands on `/ui/import/d/…/update` and shows "Your change was not made" (`internal/server/importshell.go` ~1257).
   - Fix: with htmx present, Enter in a review text field triggers the htmx update once and does not submit natively. One way is `hx-trigger="change, submit"` on the form, with the native submit prevented by htmx.
   - Without script, a plain POST must still work and redirect back to the review.
   - No path may send two requests with the same revision.
   - Test: an e2e step types in a row title and presses Enter. Exactly one update request is sent, the page stays where it was, no "not made" notice appears, and the title changed. Also a Go test of the no-script POST path.
2. **Enter in Project title imports the book.** That input carries `form="imp-applyform"` (~line 50). Enter in it must save the title (or do nothing) and never apply.
   - Test: Enter in Project title creates no project; the draft keeps the title.
3. **New-project imports.**
   - (a) The result page of an import into a new project offers "Undo this import". `undoResult` in `internal/server/importundo.go` (~175) requires a target project; use the created project.
   - (b) "Discard this import" on a finished import becomes "Remove from this list" (catalogue), and its help says the project stays.
   - (c) The duplicate warning also shows on the new-project path. `alreadyNotice` (~160) returns nil without a target. For a new-project import, look for an earlier import of the same SHA-256 into any project the caller can read, and name it with a link ("You imported this file into *X* on …"). Never count or name a project the caller cannot read; test that with a second user.
   - Tests in Go for each, and an e2e step: import as a new project, see Undo on the result page, undo, see the documents trashed.
- **Existing e2e:** e2e-import-review, e2e-import, e2e-import-chapters, e2e-import-phase4, e2e-review-ui7. a11y family: import.

## U2. The locked page

1. **Make room.** Move the locked-page code out of `web/static/js/doc.js` into a new lazy file `web/static/js/lock.js`, loaded only when the page is or becomes locked: `lockUI` (~525), Take over, the locked banner's handling, and the new re-check below. Register it like the other lazy files: a `lazyJS` row in `web_test.go` with a limit of about 1.5 KB, and listed where `#write` lists its extras. `doc.js` must shrink; report the numbers.
2. **The title on a locked page.** While locked, the chapter title field is `readOnly` (set in the lock code, and cleared when the lock is taken back). Nothing optimistic is shown in the rail or tab title for a title that cannot be saved.
3. **A title-only change is kept in the local draft.** `writeDraft()` (~218) returns early when the text has not changed (`want`). A changed title alone must also be written, and restored with the rest.
4. **A locked page re-checks.** `renew()` (~554) returns early when locked, and the visibility handler (~410) only calls `renew()`. While locked, check the lease every 30 s and when the page comes back to the front; the lease lasts 90 s (`internal/domain/docs/lease.go:10`).
   - When the lease is free, reload the chapter's text and revision from the server before enabling editing (never edit on stale text), then say "You can write here again" in the live region.
   - Never take a lease another live tab holds.
- **Tests (e2e):**
  - kill a browser context without unload, open the chapter in a new context: it is locked, then writable on its own within about 2 minutes, and it shows the server's current text;
  - typing in the title of a locked page is refused;
  - a title-only change survives a reload of an unlocked page.
- **Existing e2e:** e2e-write, e2e-write2, e2e-long, e2e-sidebar, e2e-writer-comments.

## U3. Small writing-view bugs

1. **False "changed somewhere else" bar on a move.** `doc.moved` carries no revision (`internal/domain/docs/docs.go` ~813). `web/static/js/livewrite.js` (~57–63) skips only `doc.updated` events without content.
   - Ignore `doc.moved`, and any event without a revision, for the conflict bar. Moving the open chapter must not show it.
   - Update the chapter eyebrow ("CHAPTER 2") when the chapter's number changes.
2. **Focus after a keyboard move in the tree.** After Alt+Up or Alt+Down on a row, focus stays on that row, which is now in its new place.
3. **"Fields (0 of 8 filled)"** (`web/templates/partials/entry.html` ~17) updates after a field saves in the sidebar (`lookup.js`).
4. **The comment refusal is visible.** When a selection spans paragraphs, `cwrite.js` (~46, ~228) falls back to a chapter comment and tells only a screen-reader live region (`review.js` ~25). Show the same sentence visibly in the composer.
5. **Notes created in the same millisecond sort ambiguously.** Found during U1: `TestNotesListTheRecentOnesAndSearchTheirTitles` (`internal/server/lookupui_events_test.go` ~211) failed once, then passed. The sidebar's recent notes sort by `UpdatedAt` alone, and `db.Now` has millisecond resolution, so two notes made in the same millisecond keep id order and the "newest first" assertion can fail.
   - Fix the code, not the test: add a deterministic tie-break to that sort (newest id first if ids sort by time, otherwise the position or created order). Check other "recent first" lists in the sidebar for the same pattern.
   - Test: notes with identical `updated_at` come back in a fixed, documented order. Make it fail before the fix by setting the times equal in the test.
- **Tests:** e2e steps for each. `tree.js` shares the budget with `doc.js`, so this batch comes after U2.
- **Existing e2e:** e2e-write, e2e-sidebar, e2e-writer-comments, e2e-tree (if present), e2e-shortcuts-keys.

## U4. Compile can be found

- **Command palette** (`web/static/js/palette.js`): a "Compile…" entry on every page of a project, found by "compile", "export", "docx", "pdf", "epub", "print", "manuscript".
- **The project's top tabs** (`ptabs.html`): "Compile" next to the others. Check the 390 px layout.
- **The Library card's ⋯ menu:** "Compile…" above the export item. Rename "Export as Markdown…" to "Export…" if its page offers more than Markdown; otherwise keep the name and add Compile beside it.
- **Tests:** a palette test that "export" and "docx" find Compile; e2e of the card menu.
- **Existing e2e:** e2e-palette (or whatever covers the palette), e2e-shortcuts, the library script.

## U5. The Compendium button, the sidebar's memory, shortcuts and the time zone

1. **A labelled "Compendium" button in the writing view's header,** opening the sidebar on the Compendium tab. Its tooltip names Alt+Shift+C (drawn from the shortcuts registry, not hand-typed; the drift tests enforce that). The unlabelled sidebar toggle gets a visible label or an accessible name that says what it opens.
2. **The sidebar remembers its last tab** per browser (localStorage, wrapped in try/catch) and opens on it.
3. **Shortcut text uses the person's platform.** The help and tooltips say Cmd on a Mac and Ctrl elsewhere wherever a key is named; find "Ctrl+K" style text that does not go through the registry.
4. **The writing day defaults to the browser's time zone** for a person who has not chosen one. Send the browser's `Intl.DateTimeFormat().resolvedOptions().timeZone` once (on the settings or goals save path, or the first signed-in page). It is stored as the person's zone only when they have none set. Never override a chosen zone.
- **Existing e2e:** e2e-sidebar, e2e-shortcuts, e2e-shortcuts-keys, e2e-goals (if present). Check the desktop menu chords' drift test if the registry changes.

## U6. The selection bar and the right-click items

**When it appears.** A small bar appears above a non-empty selection in the editor when the selection is finished:
- on mouseup after a drag or double-click;
- on keyup of a Shift+arrow;
- never on a collapsed caret;
- it hides on the next key that types, on Escape, on scroll and when the selection collapses.

**Rules.** Invariant 8: it is absolutely positioned in an overlay layer, never in the text flow, so nothing reflows. It never takes focus on appearing, and it never appears while keys that type are pressed.

**Contents.** Three buttons: "Add to Compendium" (opens the sidebar's New entry with the words as the name, as Alt+Shift+C does), "Look up" (the Compendium search with the words), and "Comment" (Alt+Shift+M's path). Each button's accessible name is its visible text.

**Keyboard and right-click.** Keyboard users already have the chords; the bar is reachable by Tab from the editor only if that does not break Tab's current behaviour, so check. The same three items go in the editor's right-click menu, which today has Add an alternative, Ghost this text and Send to the stash.

**Where the code goes.** A lazy file (for example `selbar.js`), with its own limit.

**Phone.** On a phone (coarse pointer), use the platform's own selection UI. Show the bar below the selection, or not at all if it would cover the native handles; decide and test at 390 px.

**Tests:**
- e2e: select by drag, and the bar shows with its three buttons; type a letter, and the bar is gone and the letter typed;
- each button does its job;
- the right-click items exist;
- no layout shift (`PerformanceObserver` layout-shift entries are zero while the bar shows and hides).

**Existing e2e:** e2e-write, e2e-sidebar, e2e-writer-comments, e2e-alts.

## U7. Import titles from the chapter line

1. **"Use the chapter line as the title".** A bulk action in the import review, offered when at least two titles hold text before a chapter label (for example "I hope Mr. Bingley will like it. CHAPTER II.").
   - It renames each such chapter to the label and what follows it ("Chapter II"), keeping the writer's own renames.
   - It shows a count, and it is one change with one Undo.
   - It goes through the draft edits like "Use title case for all".
2. **Numbering from the label.** When a pattern or label cut finds "CHAPTER II", the chapter is titled from the label ("Chapter II", title-cased per the existing suggestion rule), not numbered by its position ("Chapter 3"). Find where untitled pattern cuts are named, in `internal/imports/docxbreaks.go` or `docxpattern.go`.
3. **The `[image]` marker never leaves literal asterisks.** When the marker sits against a letter ("*[image]*APPY"), write it so Markdown renders no stray asterisks, for example with a space or a different marker form. A test reads the imported Markdown.
4. **A review row that holds a table says so.** U0 found that a Word table's text is kept as a Markdown table, but table cells are not counted as words. So a row that is mostly a table (Pride and Prejudice's List of Illustrations) showed "1 word" and no first line, and both testers left it out.
   - Such a row shows "1 table, N cells" or similar beside its word count, and its preview line shows the table's first row as text.
   - Do not change how words are counted elsewhere.
- **Tests:** a corpus-style Go test with caption-prefixed headings (the corpus must stay 25/25, and add a sample), plus an e2e step in e2e-import-chapters.
- **Existing e2e:** the import scripts from U1.

## U8. A version before a big cut

The store is `internal/domain/revisions/store.go` (rule at ~12–23, 68, 271–291): a version is written only when the newest is at least 5 minutes old, holding the text before the save.

- **Big cuts.** Also write a version, whatever the 5-minute rule says, when a save removes more than 20% of the chapter's words or more than 200 words (words before minus words after). It holds the text before the save.
- **Structural changes.** Also write one before a Split (`doc.split`), a Join, and an import into an existing project (for the documents it changes, if any).
- **Bounds.** Keep the existing caps on how many versions are kept, and say what they are. A version written for a big cut is labelled so the Versions list can say why: "Before a large cut", "Before the split". Use catalogue words.
- **Tests (Go):** each trigger writes exactly one version, the 5-minute rule still holds for small edits, and the version holds the text before the change. An e2e step deletes most of a chapter, opens Versions, sees "Before a large cut" and restores it.
- **Existing tests:** the revisions package, e2e-write2 (versions steps), e2e-split.

## U9. The Compendium

1. **The longest name wins** when names overlap ("the Guild of Lamplighters" over "the Guild"). Find the mention matcher (search for the alias matching used by `mention.list` and the underlining).
2. **Offer the first name.** In an entry for a person with a multi-word name and no alias equal to its first word, show "Also known as *Ines*? Add" (one click adds the alias). It never auto-matches. Not for places, items or organisations.
3. **"This scene" becomes "Timeline"** in the sidebar's Compendium views (catalogue `lk.*`), and **"Bible" becomes "Compendium"** everywhere it is shown, including the phone nav and "from the bible". Search the catalogue and templates.
4. **Focus stays in the sidebar after Create** in a new entry: the new entry's first empty field, so Esc returns to the text.
5. **Timeline dates.** "687-09-14" was shown as parsed and then refused; accept years under 1000 written without padding. A date error appears next to its field, not at the top of the panel.
6. **The squares in the Entries list** that look like checkboxes (`span.ent-sw`) are not controls. Make them plainly decorative (a different shape or colour swatch with no box look), or remove them.
7. **"Save and add another"** on the full entity page's create form.
8. **The project's Compendium tab.** Check what `/p/{slug}/compendium/open` and `/c/<slug>` open. If a Compendium exists, the first thing shown should be its entries; if that is already so, say so and change nothing.
- **Existing e2e:** e2e-sidebar, e2e-compendium-nav, e2e-entities, e2e-compendium, e2e-timeline, e2e-mentions.

## U10. Small things

- "1 of 64 document is" (plural).
- The orphaned "Organisation" radio in the sidebar's New entry form.
- The HTML export demotes an in-chapter H2 to h4; check what the levels should be against the export's chapter heading level.
- The phone toolbar's "›" overlaps H2 and H3.
- A scene's header shows "SCENE · , IN … · ," with empty numbers.
- Compile's title-page word count showed "About 84,300 words" for a 1,174-word book, though the file was right; find where the screen's figure comes from.
- "Continue where you left off" opens at the top; make it go through `resume.js`'s caret restore if that can be done within its budget, or say why not.
- Each gets a test where one fits.

## U11. Bigger changes

Each is built separately, after U1–U10.

1. **A slimmer header and grouped tools.**
   - Group the revision tools (ghost, stash, alternatives, split) in the format bar under one labelled menu.
   - Keep the chapter title readable at 1280 and 390 px.
   - The Compendium button (an icon in the writing header since U5) gets its visible label here, once the header has room for it. Its accessible name stays "Compendium".
   - The sidebar's tabs fit at 1280 px without clipping mid-word (wrap to two rows, or shorter labels).
   - The breadcrumb cap added in U9b (20rem from 1280 px up) also applies at 1440 px and wider, where the tabs then sit beside the trail instead of by the header tools. Revisit it in the redesign.
   - The breadcrumb must fit without ellipsis under the WCAG text-spacing override and at 200% text, so the `text-spacing` and `text-200` breadcrumb excuses in `scripts/e2e-a11y.mjs` (and the note in docs/ACCESSIBILITY.md) can be removed. The writing view at 1024 px must not overflow its tabs or overlap the breadcrumb (an overlap that was already on main before U4).
   - Screenshots before and after.
   - Decision (2026-11-25): the header stays one line. Names that do not fit are cut with an ellipsis, as on main, because a header that grew to show every name cost writing space. The 200% and text-spacing excuses in the audit stay (docs/ACCESSIBILITY.md says why). The page tabs scroll in their own box and never sit under the header tools. The sidebar's tabs wrap to three rows at 316 px; two rows are not possible there, even with shorter labels (OPEN-QUESTIONS.md).
2. **Compile with progressive disclosure.** The preset, Preview and Download sit above the fold; every other option is under "All options" (a `<details>`, open when the page is reached with options already changed). Nothing is removed.
3. **On a phone, "Saved" is shown as a word, not only a dot** (below 481 px `write.css` shows the dot alone, which predates this plan). Find room for it in the phone header. **The phone's sidebar sheet at half height,** with the text above it still visible, and the caret restored when it closes. The format strip goes behind one "Aa" button on a phone.
4. **Comment anchors in the text:** faint marks on the words a comment is on, with an off switch in the Comments tab. Writer comments phase 2 planned these marks (docs/plans/WRITER-COMMENTS.md); build to that plan.
5. **Import clean-up.** First import one plain manuscript (a .docx typed in a word processor with Heading 1 chapters, not a converted web page) and check that drop-cap repair and Gutenberg licence detection are worth building. If they are:
   - Gutenberg's "*** END OF THE PROJECT GUTENBERG EBOOK" and what follows become back matter;
   - review edits are reused when the same file is added again (offer "Use the fixes from your earlier review").

## U12. The release

- `make desktop-smoke` (it was skipped in all four import phases).
- Record bench baselines for the new metrics in `bench/baseline.json`.
- Run the full e2e suite.
- Bump the minor version, and write the changelog entry in the owner's plain voice.
- Then tag, push and watch the release workflow.
