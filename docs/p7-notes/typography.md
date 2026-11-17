# Phase 7: custom typography (F-085)

Backend and ops, the stylesheet and the two attribute hooks the pages need; the settings screens come later. Section letter AZ in FEATURES, ids F-085 and F-976 to F-984, migration `1910_typography`, i18n prefix `typography.`.

## What it is

A project's owner or an editor sets how the project's **rich (writing) view and reading view** are drawn; each person may override it for themselves. The choices are a fixed schema, validated on the server: no free CSS, no URL, no font upload, no external font.

| Field | Values | Default (nothing set) |
| --- | --- | --- |
| `font` | `serif` (Newsreader), `sans` (Instrument Sans), `mono` (IBM Plex Mono), `system-serif`, `system-sans`, `system-mono` | the person's own editor font |
| `size` | 100 to 140 in steps of 5: a **percentage of the reader's own text size** | 100 |
| `line_height` | 1.3 to 2 in steps of 0.05, as the string `"1.3"`, `"1.35"` … `"2"` | the person's own editor setting |
| `measure` | 40, 45, 50, 55, 60, 66, 70, 75, 80 characters | the person's own editor setting |
| `paragraphs` | `spacing`, `indent` | spacing (a 0.95 em gap, no indent) |
| `scene_break` | `ornament` (three asterisks), `rule` (a short rule) | ornament |

Only fonts already in `web/static/fonts` are offered (the font budget in `docs/ARCHITECTURE.md` is 1 to 4 files and there are four), plus the system's three stacks. No file was added.

## Where it applies, and where it never does

On screen, when a page loads, in the **writing view** (`/p/{slug}/d/{id}`) and the **reading view** (`/p/{slug}/read…`). It is never applied live into an open editor (nothing reflows while typing), never to a share link's page, a compile preview, an export, print, or the static site: those have their own typography options. The stylesheet is wrapped in `@media screen`, so printing either view from the browser does not use it. Tests: `TestTypographyNeverReachesSharesExportsOrTheSite`; the share page, the Markdown and HTML exports carry none of it.

## The mechanism, chosen by measuring

**Data attributes on the paper element that a pre-built stylesheet reads**, not a generated stylesheet per project.

- The existing `.paper[data-size|data-measure|data-lh|data-font]` rules already work this way, so there is nothing new to learn and nothing to cache.
- A route such as `/p/{slug}/typography.css` would need a private cache, an ETag that includes the person's override and the project's, a service-worker answer for the offline reload, and one more request before first paint. Data attributes need none of that: the page carries the result, and the offline reload (the service worker keeps every file under `/static`) draws the same.
- The combinations are few: the rules are one per value, not one per combination.

**Byte cost:** `web/static/css/typography.css` is **4,159 bytes raw and 1,358 bytes gzip**. It is linked **only by a writing or reading view whose page carries typography** (`{{if .Data.Prefs.TypoAttrs}}` in the two heads), so a project with none, every other page, and the offline shell's cost for those pages are exactly what they were: the writing view's CSS on load stays **21,963 bytes gzip** and the reading view's 14,564. With typography set the writing view is about 23,321 against the 25,600-byte budget (about 2.3 KB left).

**How it reaches the page.** `typography.Settings.HTMLAttrs()` is the one place a value becomes HTML. It writes only: `data-tf="sys-serif|sys-sans|sys-mono"`, `data-ts="105…140"`, `data-tlh="130…200"`, `data-tm="40…80"`, `data-tp="indent"`, `data-tb="rule"`, each from a fixed list and only when it differs from what ships. The three bundled faces go through the existing `data-font` attribute. `editorPrefs.TypoAttrs` carries the string into the two templates (`<article class="paper" …data-font="serif"{{.Prefs.TypoAttrs}}>`), which is empty when nothing is set; the same value decides whether the stylesheet is linked.

- **Byte for byte as before:** with no typography the page has no `typography.css` link, and the tag is exactly `<article class="paper" id="paper" lang="en" dir="ltr" data-size="19" data-measure="66" data-lh="165" data-font="serif" aria-labelledby="doc-title-h">`, and a project that was set and then reset renders the same bytes as one never set (both the writing and reading views). `TestNoTypographyRendersAsBefore`.
- **No inline style, no `style=`.** Nothing is written to a `style` attribute or a `<style>` element; the strict CSP is unchanged.
- **The tests that stand for "fuzz the ETag and the CSS":** there is no per-project CSS to fuzz, so the fuzz is over the resolver's output (`TestHTMLAttrsOnlyEverCarryAllowListedValues`: 20,000 random settings made of braces, `url(`, `expression(`, quotes, `@import`, NULs and numbers; every output matches a regular expression of the allowed attributes and none contains `}`, `url(`, `expression(` or a quote), and over the stylesheet (`TestTheStylesheetHasARuleForEveryValueAndNoOther`: every value on every list has a rule; every attribute value in the sheet is one the server can write; no `url(`, `@import`, `@font-face` or gradient; screen only). There is no ETag or cache header to test.

## Precedence (decided)

Highest first:

1. **The person's own typography override** (`account.typography.set`) wins over everything, field by field.
2. **A person's explicit editor settings** (Settings, Editor): `editor_font`, `editor_line_height` and `editor_measure` whose stored value is **not the application's default** (compared with `account.Defaults()`) win over the project's typography, because they may be an accessibility choice (a tall line height for dyslexia, a narrow measure for low vision). A Settings form that saves every key at its default does not count as a choice.
3. **The project's typography** applies only where the person has chosen neither.
4. What the application ships.

A person who wants a *default* value in the face of a project pins it through the override (the override of `1.65` beats a project's `1.3`). The rule is `typography.Effective(project, person, editorPrefs)`, used by the pages and by the ops (`effective`); `TestPrecedenceEveryCombination` walks project set or not, editor value default or not, override set or not, for each of typeface, line height and measure.

**A project can never make text smaller, tighter or narrower than a reader chose.** Line height and measure and typeface a reader set in Settings are kept whatever a project sets. Size is the one field that composes instead of competing: the project's percentage is of the person's own size and is 100 to 140, so it can only keep or enlarge it (kept, as decided). A project may still make text larger, and may set a measure or line height for the people who chose nothing.

The Settings screen should say: "Your own editor settings are kept when a project sets a different line height, measure or typeface. Your typography override wins over both."

## Accessibility minimums, enforced in validation

Written in `docs/ACCESSIBILITY.md` and enforced by the lists (no value below them can be stored): size never below 100 percent of the reader's own, so the smallest body text is the reader's own smallest (16 px) and it scales with browser zoom; line height never below 1.3; measure never above 80 characters. Colour is not part of it: the five themes, the contrast pairs and forced colours are unaffected (a scene-break rule is a border, which forced colours keeps; the check-contrast script reads no new token). Size is a multiple (`calc(var(--prose) * var(--ts))`), so nothing is a fixed px that defeats zoom.

**DESIGN.md said "no first-line indents in the editing surface".** A project's owner or editor may now choose it, and a person may; it is drawn in the writing view too because it was asked for. DESIGN.md is amended in the same commit. Nothing is indented until someone chooses it.

## The ops

| Op | Route | Who |
| --- | --- | --- |
| `project.typography.get` | `GET /api/v1/projects/{id}/typography` | any member |
| `project.typography.set` | `PUT /api/v1/projects/{id}/typography` | owner or editor; replaces what the project had (a field left out is not set; all fields empty is a reset) |
| `project.typography.reset` | `POST /api/v1/projects/{id}/typography/reset` | owner or editor |
| `account.typography.get` / `.set` / `.reset` | `GET`/`PUT /api/v1/me/typography`, `POST …/reset` | the caller |

`reset` is a POST (a `DELETE` would have needed a Permanent decision; it removes a setting that can be chosen again). The project ops follow the usual rules: a trashed project refuses set and reset (409), a protected project is refused until unlocked (403), a dry run stores nothing, a stranger gets the 404, an unknown field or a value off the list is a 4xx and stores nothing. **A key with write scope may set the project's typography** over REST. This is deliberate: it is a display setting like the others, it cannot go below the readable minimums, and it is the one place an agent key can change what people see on every page of a project, which is why it is stated here. It is **not an MCP tool** (`project.typography.*` are listed in `excludedOps`: an agent reads and writes text, not how a person's page looks), and `account.typography.*` is an excluded group. A key pinned to a project cannot reach the account ops, and `project.typography.get` for **any key** returns no `mine` and an `effective` that is the project's own (a person's choice of large text or tall lines can hint at an access need; `TestReviewPinnedKeyDoesNotLearnThePersonalOverride`).

The answer of every op carries `project` (what the project sets, absent for the account ops), `mine` (the caller's override), `effective` (the project's choices with the person's editor settings and override applied, as above) and `schema` (the lists, the notice), so a screen needs no second call.

Audit: `project.typography.set` and `.reset` are recorded. No event is emitted (the mirror, Git and webhooks have nothing to say about a page's look), so an open page picks a change up on its next load.

## What a screen needs

- **Routes:** the ops above, from the project's settings page (owner or editor; a reader sees the effective result read-only) and from the account Settings page. No fragment routes were built. A preview in the form can use the `schema` and `typography.sample`; the live sample can set the same attributes on a `.paper` element (no inline style: change the `data-t*` attributes, `typography.css` does the rest, and add the stylesheet link to that page).
- **Words:** `en.json`, prefix `typography.`: `title`, `project_h`, `mine_h`, `project_hint`, `mine_hint`, `notice` (the same sentence the schema carries), `font` and its six values (`font_serif`, `font_sans`, `font_mono`, `font_system_serif`, `font_system_sans`, `font_system_mono`), `size`, `size_hint`, `size_value`, `line_height`, `measure`, `paragraphs`, `para_spacing`, `para_indent`, `scene_break`, `break_ornament`, `break_rule`, `unset`, `unset_project`, `unset_default`, `sample`, `save`, `reset`, `saved`, `was_reset`, `refused`.
- **Form behaviour:** one control per field, each with a "Not set" choice (an empty value; send the whole form, an unset field is simply left out). The size control shows percentages and the hint that it can only keep or enlarge. Say plainly under the form that exports, compile, print and the site do not use it.
- **Offline:** the settings pages are ordinary pages; saving needs the network. The pages that use it need nothing extra offline: the attributes are in the HTML and the stylesheet is in the shell cache.

## Not built, and what was not verified

- No settings screen, no live sample, no event for open pages.
- **Rendered once, in Chromium, by `scripts/e2e-typography.mjs`** (a new real check, not yet in the Makefile's `e2e` line; the coordinator adds it). What it measured is in the section below. **Not looked at by a person:** how any combination reads, forced colours with typography set, other browsers, the system faces on other platforms, the 200 percent text and spacing checks of the accessibility audit with typography set.
- `.chapter`, `.scriobh-prose` and `.doc-plain` are the three containers the size rule names. A screen that adds another prose container must add it to the rule.
- Typography is not part of a project's export or its Git mirror, so it does not travel with a copy of the book; it is in the database and its backups.

## Rendered measurements (Chromium 151, `scripts/e2e-typography.mjs`, a 2000 px wide window)

- **Measure, in characters a line** (the line's width over the width of 1ch in its own font; the unscaled default measures 64.6 for a stored 66 in this browser, so the check allows 4 percent):

| Size, measure | Writing view | Reading view | Plain-text stand-in |
| --- | --- | --- | --- |
| 100 %, 40 | 38.5 | 40.1 | 19 px (editor 19 px) |
| 100 %, 80 | 78.6 | 80.1 | 19 px |
| 140 %, 40 | 38.5 at 26.6 px | 40.0 at 26.6 px | 26.6 px (editor 26.6 px) |
| 140 %, 80 | 78.0 at 26.6 px | 58.3 (the column leaves 864 px; see below) | 26.6 px |

- **Before the fixes** the same pages measured: writing view at 140 %, 27.2 characters for 40 and 55.4 for 80 (the page is sized in `ch` of its own unscaled font, so the measure shrank by the scale); reading view at 100 %, 63.5 for 80 (the reading page is capped at `--page-w` whatever the setting), and the scaled text made it 44.8 at 140 %. Both are fixed by sizing the page in the text's own scaled unit (`html body.write .paper[data-ts]`, `html .reading-paper:is([data-tm], [data-ts])`).
- **What is still limited by the layout:** at 140 % and 80 characters the reading view's page needs about 1,190 px and the layout leaves 864 px beside the review margin (`.reader.has-margin`, a grid of the text and a 23 rem comments column, 80 rem wide at most) on a 2000 px screen, so the line is as wide as the column (58 characters at that size). That is the column, not the setting; it is stated, not changed.
- **A person's own editor measure in the reading view** (52 to 82, or full width) is still capped at `--page-w` there unless the project or the person sets a typography size or measure, when the page is sized in the text's own unit as above. A reader's *full width* is never touched by a size: `data-measure="full"` keeps both pages exactly as wide as before (writing 1710 px and reading 664 px, with and without a project size, in the run), unless the person's own override sets a measure. `size` alone leaves the writing view at 64.1 characters and the reading view at the column's 58.3 at 140 percent (66 characters would need about 950 px).
- **Unexplained:** the writing view falls about 1.5 characters short of the stored measure at every measure and size (38.5 for 40, 78.6 for 80, and 64.6 for the unscaled default 66). It is the same with and without typography, so it is how the page's width and a `1ch` probe relate in this browser, not something typography adds; I did not find the cause.
- **Scene break:** with `rule` the reading view's separator (`hr.scene-break`, a sibling of `.r-prose`) and the editor's `.scene-break` both compute `::before` content `""` with a 1 px solid top border; with the default they show the asterisks.
- **Indent, face, line height:** at 120 % (22.8 px) the second paragraph's `text-indent` is 34.2 px (1.5 em), the first and the one after a scene break 0, the paragraph gap 0; the face computes to `ui-monospace, "SF Mono", Menlo, Consolas`; the line height ratio 1.80.
- **Precedence in a browser:** a reader with `editor_line_height 1.9, editor_measure full, editor_font sans` under a project of `1.3, 80, mono` computes a line height of 1.90 and Instrument Sans, and the page carries no `data-tlh` or `data-tm`; with the personal override `1.3` set it computes 1.30.
- **No typography, no link:** with nothing set the page has no attribute on the paper and no link to `typography.css`.
- No console, CSP or off-origin finding in the run.
