# Phase 7 screens: Pandoc

Branch `ui-7-pandoc`. Features F-952 to F-954 (section AX). One small additive backend change (below); every call otherwise goes through `pandoc.config.get`, `.set`, `.test`, and the existing import and compile ops.

**No real Pandoc was used.** The Go tests use the stand-in in `internal/pandoc/pandoctest`; the browser script uses `scripts/fake-pandoc.mjs`, a shell script written for it (version `pandoc 3.2`, an import that prints a known paragraph and the first line of the file, an export that writes the compile Markdown behind a line naming the writer, a `mode` file that makes it fail with a canary in its error text). What these show is how the pages call the bridge and what they do with its answers. Nothing here says what a real Pandoc accepts or writes.

## What exists

| What | Where |
| --- | --- |
| The page | `GET /admin/pandoc` (`internal/server/pandocui.go`, `web/templates/admin-pandoc.html`), a tab of the console beside Email; administrators only, 404 for others (the same `adminOnly` as the other console pages) |
| Save and test | `POST /ui/admin/pandoc/save` (switch, path, memory in one form; a refusal is a 422 page with the op's sentence on the path field, the typed values kept; success redirects to `?saved=1`) and `POST /ui/admin/pandoc/test` (the result is drawn in the answer, not stored) |
| Compile | `compileData.Pandoc` from `Server.pandocUsable` (the service's `Status`, which caches the program check for a minute); `data-pandoc` on `#cmp`; `compile.js` leaves odt, rtf, latex, rst and mediawiki out of the format list unless it is `true`, keeps a format a preset already uses (marked "Pandoc is switched off"), and under a Pandoc format shows the note that Pandoc makes the file |
| Import | `importData.Pandoc` the same way; `import.html` has the radio and a panel only when it is true; `import-formats.js` (extended, not forked) reads the file, takes the format from the extension, and sends `pandoc` and `pandoc_format` (plus `docx_headings` and `pictures`) through its `opts` hook, with no change to `import.js` |
| Tests | `internal/server/pandocui_test.go` (who sees what, off and on, the refused path and old version, the test with a failing program never showing its output, the formats on Compile and import following the switch, the pseudo-locale sweep of the page in four states and of the import panel); `scripts/e2e-pandoc.mjs` (14 steps) |
| Accessibility walk | `import-pandoc`, `admin-pandoc`, `admin-pandoc-tested`, `admin-pandoc-off`, `admin-pandoc-restored`, `admin-pandoc-refused`; the walk now switches the bridge on against the stand-in when it seeds, so the Compile and import entries are seen with the extra formats |

## Decisions

**The Compile and import pages decide on the server.** `pandoc.config.get` is for administrators, so a writer's page is told by the server whether the bridge is on and its program passes. `Status` can run `pandoc --version` at most once a minute; both pages call it on every load, which is a database read and a stat.

**A page open while the bridge goes off still works safely.** The Compile page's export and the import preview answer 409 with the server's sentence (`pandoc_off`, `pandoc_unusable`), which both pages already show as they show any refusal; the import page keeps the chosen format and the radio. The script covers the import case.

**The restore banner needed a mark, so there is one (the only backend change).** `restore.go` only flipped `pandoc.enabled`, so a page could not tell a restore from an administrator's own switch-off, and a restored server that finds Pandoc on `PATH` (empty path) would have had no banner. The restore step now also writes the setting `pandoc.restore_paused` = `1`; `pandoc.config.set` with `enabled: true` writes `0` (saving while off, a path change or a test leave it); `ConfigOut.restore_paused` (new field, true only while the mark is set and the bridge is off) feeds the page's note. Test: `TestRestoreLeavesAMarkOnlySwitchingOnClears` in `internal/pandoc` (hand switch-off is no restore, a restore sets it whatever the path, saving while off keeps it, switching on clears it), and the server test and the browser script check the note's presence and absence in each state. The browser script writes the mark with the `sqlite3` program (skipped, with a line saying so, where there is none); the real restore step is run in the Go tests. This changes `pandoc.config.get`'s output schema, so the generated snapshots (`internal/cli/snapshot.json`, `internal/mcp/tools.snapshot.json` if it lists it, `skills/`, `docs/API.md`) need `make skill`; I did not regenerate them. No migration (a settings row). `docs/DATA-MODEL.md` is not changed: the setting sits with the other `pandoc.*` keys.

**The test shows only the bounded sentences.** `TestOut.Problem` is one of the runner's fixed sentences; the page prints it and the format and milliseconds. A test fails if the stand-in's stderr canary reaches the page.

**Words.** The new page, the import panel and its labels are all in the catalogue (`pandoc.` keys, ten `pandoc.fmt_*` names, two `cmp.js.` keys for Compile). The existing Compile page strings were already catalogued; the import page and `import-formats.js` are literal English, and I left that as it was: only the Pandoc panel (in the template) is catalogued. The import script's messages for the Pandoc file ("Reading ... ", the 34 MiB refusal, "A document converted by Pandoc") are English like the rest of that file. The pseudo-locale sweep covers the Pandoc page and the Pandoc panel, not the whole import page.

## Budgets

`import-formats.js` 3,449 bytes gzip (limit 3,584); `compile.js` 6,825 (limit 8,192); `admin.css` 987 (the page's own sheet, far under the 25 KB per-page limit). No new file, so no new row in the lazy-JS table; one row in ARCHITECTURE says this.

## What was looked at

Real Chromium screenshots, read as images: the tested state (desktop light), 390 px dark and 390 px light, the `xx-rtl` page, the import page with the panel open, the Compile page with a Pandoc format chosen, the refused state (an old version), the restored state with its note, and the import panel under `xx-rtl`. The plain off state and the plain on state before a test were taken but not opened separately; the tested page contains the on state. Changes made from looking: the "State" heading was a bold default heading and is now a small heading of its own class (`.pd-h3`, so the console's diagnostics page is untouched); in `xx-rtl` the program's path lost its leading slash (a bidi effect), so the path and its field are `dir="ltr"`; in the import panel the format select's hint sat inside its field and the select's arrow landed beside the hint, so the hint is a note below the field as the other panels have it. The refused page puts the op's sentence at the top of the Settings section (tied to the path by `aria-describedby`), not under the field; I left that as the Git page does it.

## Run

- `go vet ./...` and `gofmt -l .`: clean.
- `go test ./... -count=1 -p 2`: every package passes except (1) `cmd/scriobh-skillgen` `TestCommittedSkillIsCurrent`, which fails until `make skill` is run (the new `restore_paused` field of `pandoc.config.get` is in the schema; stale files named: `docs/API.md`, `internal/cli/snapshot.json`, `internal/mcp/tools.snapshot.json`, `skills/scriobh/SKILL.md`, `skills/scriobh/mcp.md`), and (2) timing tests in `internal/imports` (`TestXMLManyChildrenAndAttributesAreNotBuilt`, `TestGhostMarkdownCostIsOneBudget`, and `TestGhostMarkdownBombs`; the output was cut at 30 lines, so there may be more in that package) that missed their time budgets (17 s against 15 s and 12 s) while two other engineers' builds and tests were running on the same machine. I did not touch that package; they were not re-run in isolation.
- `go test -race ./internal/pandoc/...` passes (126 s); `go test -race ./internal/server -run Pandoc` passes.
- `cd scripts && node e2e-pandoc.mjs --shots DIR`: 15 of 15, no console, CSP or off-origin findings. The "after a restore" step needs the `sqlite3` program to write the mark and prints a skip line without it.
- `node e2e-compile.mjs` 12 of 12, `node e2e-import-formats.mjs` 8 of 8, `node e2e-admin.mjs` 5 of 5 (each run once after the last template change).
- `node e2e-a11y.mjs --only pandoc` (import-pandoc, admin-pandoc, -tested, -off, -restored, -refused), `--only import` and `--only compile`, one at a time: 0 violations each. The walk now switches the bridge on against the stand-in when it seeds, so these existing entries were walked with the extra formats and the sixth radio. The full walk was not run.

## Not verified

- A real Pandoc; a real restore (the off-with-path state was produced by switching off in the page).
- Safari and Firefox; real phones; a screen reader (the accessibility walk reads the browser's tree only).
- The offline shell for these pages: the worker keeps only `/static` and `/offline`, as for every page, and the kit blocks service workers, so "reload with the network cut" was tested as the import page's lost-network behaviour (the sentence appears and the choices stay), not as a worker-served reload.
- The desktop smoke test (ephemeral loopback port): the pages use only relative addresses; the smoke script was not run.
- The full `e2e-a11y` walk: only the entries named above were run.
- The sweep: the Go pseudo-locale test fails when a literal word is put in the page (tried with one), but it covers the page in four states and the import panel only.

## For the coordinator

`docs/FEATURES.md` section AW still says no screen is built and F-934 is "Not built: screens for the Pandoc settings, the import's format picker or the export's format list"; those are now built (F-952 to F-954 in section AX). I did not edit AW.
