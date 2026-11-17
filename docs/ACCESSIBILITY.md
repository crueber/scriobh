# Accessibility

What standard Scríobh holds itself to, what is checked by a machine on every run, what has to be checked by a person before a release (and has **not** been), the places we know fall short, and how to tell us about another.

This page is a record, not a claim. Nobody outside the project has audited Scríobh, and no screen reader, switch device, high-contrast Windows or phone was used to build or check what is described here.

## The standard

**Target: WCAG 2.2, level AA.** We build to it and test against it. We do not claim conformance: a claim needs an audit by people who use assistive technology, and that has not happened. Where this page says "passes", it means "passes the automated check named next to it, in Chromium". Where the standard asks for something a script cannot judge (whether alt text is good, whether a heading says what follows, whether the reading order makes sense aloud), it is listed in the manual checklist below and is unproven until someone ticks it.

The rules in [DESIGN.md](DESIGN.md#accessibility) are the product's own and are stricter in places than AA (a 44 px touch target is the design; AA asks for 24):

- every control is reachable and operable by keyboard, in a logical order, with a visible focus ring that is never removed without a replacement;
- the manuscript tree is a real `role="tree"`, and every drag has a keyboard equivalent;
- save state is announced through a polite live region, debounced, and is never colour alone;
- the writing surface carries its accessible name and the document's language, its decoration layer is `aria-hidden`, and Source view is a real editable fallback;
- zoom to 200% without horizontal scrolling; 44 x 44 px targets on touch;
- "reduced motion, high contrast, and dyslexia-friendly font are settings, not media queries alone": of the three only reduced motion is built (F-283); high contrast (F-291) is the system's forced colours and no theme, and FEATURES records no dyslexia-friendly face (F-285);
- focus returns to where it was after a dialog or popover closes.

## What a machine checks

### `scripts/e2e-a11y.mjs` (`make a11y`, and the last step of `make e2e-fast`)

It builds the development server, signs in through the stub issuer as an owner, an editor (who is also the instance administrator) and a reviewer, seeds a book (chapters, a note, alternatives, comments, a linked Compendium with entities, events and research, share links, a webhook, an AI provider that is a fake model server on loopback) and walks **156 pages** (page states; 87 when the audit was first written, the rest added with the screens since, six of them the cover page): the sign-in, 404 and offline pages, the library and its archived view, settings, AI providers, Connect, both import pages, every project tab for the owner, the editor and the reviewer (the writing view, outline, cards, status board, reading view, research with its board, tags and a note, scratchpad, Compendium, entities and a sheet, timeline, goals, search, attachments, review, share links, compile with the print options open, static site, webhooks, people, Git sync, import, the project's AI page, and the cover page as the owner meets it with a page-mode front and an image back, with a wrap, with no layout, as a reviewer, as a draft after a picture was added and as a refusal with no description), a standalone Compendium's pages, the library's trash, the new-project, export and delete forms, the administrator's console and disable page, and a share link's locked and open pages. The same reviewer is then made a reader of the book and seven pages are walked again. It uses Chromium's accessibility tree (`Accessibility.getFullAXTree`) for names and landmarks and the DOM and computed style for the rest.

| Group | What is checked | Where |
| --- | --- | --- |
| Structure | exactly one `main`; every `nav` and named landmark carries a name, none twice; one `h1`; no skipped heading level (the author's own headings inside the text are not the page's outline); a title that is not just the product's name and not another page's; `lang`; no duplicate `id`; every `aria-*` reference and `label for` resolves; no positive `tabindex`; nothing focusable inside `aria-hidden`; `alt` on every image; SVG hidden or named; iframes titled; data tables have header cells; radio groups named; composite roles in their parents; tabs carry `aria-selected`; a viewport that allows zoom; links inside running text are not marked by colour alone | 1280 and 390 px, light and dark |
| Names | every button, link, field, listbox, tree, dialog, toolbar, tab and so on has an accessible name in the browser's own tree; a field named only by its placeholder fails; three or more controls with the same name and nothing (a description, a named group) to tell them apart fail | the same four |
| Keyboard | the first control is a skip link that points at `#main`, is visible when focused, moves focus into `main`, and the next Tab stays in `main`; Tab through the whole page and round (every tabbable control is reached, no stop repeats, no cycle inside part of the page, Shift+Tab does not stick); every stop is `:focus-visible`, in the viewport, and not under a sticky bar; the focus indicator (outline, ring, or a change of border or background) is at least 3:1 against what is behind it and an outline is at least 2 px | light and dark at 1280 px, dark at 390 px |
| Dialogs and panels | the account menu, the command palette, the new-project dialog, the chapter jump, the inspector, the writing partner's menu: opens by keyboard, focus goes in, a modal does not hand focus to the page behind it, Escape closes, focus returns to exactly where it was; what is inside each is scanned like a page (names, contrast, targets) and walked by Tab in light, dark and forced colours, so the palette's and the jump list's own fields are judged; all eight tabs of the inspector (details, versions, in this scene, alternatives, stash, comments, lab, AI) likewise | writing view and library, 1280 px; the inspector also at 390 px |
| Contrast | every run of text at 4.5:1 (3:1 for large text); placeholders; the edge or fill of every field at 3:1 against its surroundings (WCAG 1.4.11) | both themes, both widths |
| Size | targets at least 24 x 24 CSS px everywhere (the 2.5.8 spacing exception applied); at 390 px on an emulated touch device (`pointer: coarse`) every button, tab, menu item, select, checkbox, radio and text field is at least 44 x 44; inline links in a sentence are exempt | 390 px |
| Reflow and zoom | no sideways scroll at 320 px wide (400% of 1280) or 640 px (200%); at 200% text (every element's font size doubled in place, because the stylesheets set type in px and a browser's text-only zoom is emulated this way) no newly clipped text, no sideways scroll, sticky bars under half the window; under the WCAG text-spacing override (line height 1.5, letter 0.12 em, word 0.16 em, paragraphs 2 em) no newly clipped text | 1280, 640, 320 px |
| Motion | with `prefers-reduced-motion: reduce` nothing is animating or has a transition longer than 50 ms (Web Animations and computed style) | 1280 px |
| Forced colours | with `forced-colors: active` every focus stop has an outline (shadows and colour are dropped by the system) and every selected, current, pressed or checked item differs from its siblings by shape (border, underline, weight or a mark), not by colour | 1280 px |
| Announcements | the writing view's `#live` and `#toasts` regions exist as `status`; cutting the network and typing changes the visible save state in words and is announced in the live region; the editing surface is `role="textbox"` with `aria-multiline`, a name and a language; the writing partner's preview and proposal are scanned like any page, the diff marks removed words with a strike and added words with an underline, and the proposal has an Accept and a Reject by name | writing view, 1280 and 390 px |

The script is checked against itself first: four small pages built to break the rules (no names, a clipped box, a bar over a link, a dialog that does not take focus or close) run before the walk, and the run fails if any rule did not fire. Every rule has such a control except `focus-visible`, which a keyboard Tab always satisfies in Chromium. A part of the audit that cannot run (a page that never loaded, a flow that broke, an override that did not apply) fails the run too, rather than being skipped.

Other options: `--only NAME` (page ids contain NAME), `--dump DIR` (each page's accessibility tree, landmarks and headings, and a screenshot where the writing partner flow fails), `--json FILE`, `--quick` (structure at 1280 px light only, a few seconds a page, for iterating on a fix), `--report-only`. The run takes about 8 minutes (8 minutes 29 seconds through `make a11y` on the last full run). It exits 1 while any violation remains that the `ALLOW` list at the top of the script does not excuse; each entry there carries its reason, and the same reasons are under Known gaps.

### Other checks

- `scripts/check-contrast.py` (`make contrast`, in CI): every token pair in every theme, from the stylesheets. It knows nothing about pages; the audit above checks the rendered result.
- `scripts/csp-smoke.mjs`: no console error, no policy violation, no off-origin request on the main pages.
- `internal/server/web_test.go`: every template carries `lang`, the skip link and `main`; the stylesheet and script budgets.
- The editor's own tests (`web/editor`) and `scripts/e2e-write*.mjs`, `e2e-outline.mjs`, `e2e-review.mjs`, `e2e-compile-print.mjs` and others check keyboard paths, focus after a swap, live regions and 390 px on their pages. They predate the audit and are not repeated by it. `scripts/e2e-covers.mjs` does the same for the cover page: the keyboard alone at 390 px (shape, mode, file, description, Save), the position grid's arrow keys, targets of 44 px, no sideways scroll, and the page with no script.

### What the machine cannot see

Chromium only, and in the browser only the Paper and Ink themes were walked (the other three are covered by the contrast script, from their tokens). Its accessibility tree is not VoiceOver's, NVDA's or TalkBack's reading of it. The contrast check judges flat colours: text over a picture is skipped. Forced colours is the browser's emulation, not Windows high contrast, and the check is that state differs by shape; how it looks is not judged. Text zoom is emulated. Alt text, link text and heading text are checked for existing, not for being good. Pages in a state that needs a flow to reach were not walked: an error after a failed save, a half-finished import, an attachment's own page, the first-run page that claims administration (it redirects once an administrator exists), the Git force-push confirmation with a remote that has diverged (its 409 page was walked), a conflict. The trash, the new-project, export and delete forms, the timeline event form, an entity merge and the administrator's disable page were.

## Custom typography: the readable minimums (F-085)

A project's owner or editor, and each person for themselves, may set the typeface, size, line height, measure, paragraph style and scene-break style of the writing and reading views (`project.typography.set`, `account.typography.set`). The server accepts only values from fixed lists, and the lists stop at these limits, so no combination can go below them:

- **Size** is a percentage of the reader's own text size (Settings, Editor: 16 to 24 px), 100 to 140 in steps of 5. It can keep the reader's size or enlarge it and never shrink it: a project cannot make text smaller than the person chose for themselves. The smallest body text that can result is therefore the reader's own smallest, 16 px, and it scales with browser zoom like every other size here.
- **Line height** is 1.3 to 2 times the size, in steps of 0.05; never below 1.3 for body text (WCAG 1.4.12 asks that a page survives 1.5, which the text-spacing check in `e2e-a11y.mjs` still applies on top of whatever is set).
- **Measure** is 40 to 80 characters (WCAG 1.4.8's ceiling is 80); the application's own wider "full width" is the person's own editor setting, not available to a project.
- **Typeface** is one of the three faces already shipped or the system's serif, sans or monospace. No font is downloaded.
- **A project can never make text smaller, tighter or narrower than a reader chose.** A reader's own typeface, line height and measure from Settings, Editor (any value that is not the application's default) are kept whatever a project sets; only the reader's own typography override beats them, and only the reader sets that. Size is a percentage of the reader's own and can only keep or enlarge it. Where the reader chose nothing, the project's values apply, within the limits above.
- Colour is never part of it. The themes (Paper, Sepia, Ink, Slate, Moss), the contrast pairs and forced colours are untouched; a scene-break rule is a border, which forced colours keeps.
- It applies when a page loads, never to text being typed, and only on screen: print, exports, compile, share links and the static site never read it.

Checked by tests (`internal/domain/typography`, `internal/server/typography_test.go`): every value on the lists, every value refused, the stylesheet's rules against the lists, the rendered attributes, and every combination of project, editor setting and override. `scripts/e2e-typography.mjs` renders both views in Chromium and measures characters a line at 100 and 140 percent, the scene break, the indent and the precedence. **Not checked:** how any combination reads (a person must look at the writing and reading views with the extremes set: 140 percent, 2 line height, 40 characters; 100 percent, 1.3, 80; indent; system monospace), the 200 percent text and spacing checks of `e2e-a11y.mjs` with typography set (the audit walks pages with none), or a screen reader.

## The Writing Studio's charts (F-1101 to F-1105)

No chart on `/studio` is a picture alone, and none needs script. The four sparklines and the words-written chart are inline SVG with `role="img"` and a title that holds their numbers (the chart also has a description, a sentence beneath it, and the same numbers as a table behind "The numbers as a table"). The bars are `<progress>` elements, each named, with their figure beside them as text. The activity calendar is a table: every row is named for its weekday, every cell holds a sentence with the date and the words ("3 Nov 2026: 420 words, daily goal met"), and it sits in a labelled region that takes focus and scrolls by key when it is wider than the screen. A day's level is the size of its square as well as its tint, a goal day has a ring and today a dashed outline, and the legend says the steps in words, so nothing depends on colour; in forced colours the sizes, the ring and the meters are drawn in system colours. A smaller period is written in the same ink as a larger one, and an empty day has no mark. Not checked: how a screen reader reads a table of 24 columns (a person may prefer the chart's table, or the CSV).

## Keyboard map

The map is in the product rather than here, so that it cannot go stale:

- **Command palette**, every action by name: `Mod+K` (inside the editor `Mod+K` is Link; use `Mod+Shift+P` or `Mod+/`). F-164 in [FEATURES.md](FEATURES.md).
- **A generated shortcut reference** (`?`, F-166) is planned and not built; the palette shows the shortcut of every command that has one.
- **The writing view**: formatting shortcuts and the Source toggle (F-063), `Alt+PageUp` and `Alt+PageDown` between documents, `Alt+Shift+I` for the writing partner's menu on a selection, the toolbar as one Tab stop with arrow keys.
- **Lists that are trees, tabs, listboxes**: one Tab stop each (a roving tabindex), arrow keys inside; the outline, the manuscript tree and the boards move an item with Alt+ArrowUp and Alt+ArrowDown (and the Move commands of DESIGN.md) as the keyboard form of dragging.
- **Skip to content** is the first Tab stop on every page, including a share link's.
- **Escape** closes a dialog, a popover, a panel or a preview, and puts focus back.

## How a screen-reader user meets the product (reviewed, not heard)

No screen reader was run. What was done instead: the accessibility tree of all 87 pages of the first walk was dumped (`--dump`), and the tree of the writing view and ten others (library, settings, outline, compile, review, reading view, a share link locked and open, the project's AI page, the Compendium's field list, Connect) was read in order as a reader would meet it: landmarks, then headings, then the tree itself. This finds names, order and structure. It cannot find how a particular reader pronounces, announces or skips them.

What the tree says for the writing view, in reading order: a skip link; the banner (the wordmark, a Breadcrumb navigation, the Project navigation, Versions and Details buttons, the save state, the palette and account buttons); the Manuscript complementary landmark holding a tree named "Manuscript, 4 documents" whose items are named by number, title and word count, and the current one is `selected`; the main landmark holding the Formatting toolbar (groups named Text, Headings, Blocks, Insert, Alternatives, Writing partner, View; toggles are `pressed`), then the document as an `article` named by its title containing a level-1 heading with the title field in it, then the editing surface (`textbox` "Document text", multi-line, in the document's language), the Neighbouring documents navigation, the Document status group, and an empty polite status region that carries announcements. Comments are in a complementary landmark in the reading view, each comment a list item named by its author and time, its anchor in the text a `mark` that points at it with `aria-details`.

Announcements (read from `doc.js`; only the Offline one was driven and heard back by the audit, the others are the same code path): `#live` (a polite status) receives "Offline", "Not saved", "Conflict", "Locked", "Signed out", and "Saved" after any of them, debounced by 1.2 s so that typing is not narrated; an ordinary save is silent by design. Toasts are in a polite status region, errors in forms are `role="alert"`.

In the editing surface, what is drawn as a decoration is not in the tree (the decoration layer is `aria-hidden`, as DESIGN.md says): the alternatives' wavy underline and circles, ghosted text and the stash are visual only, and in the dump "Sentence 1.5", which has two alternatives, is plain text. The Alternatives tab of the inspector lists every alternative and is the accessible way to find them; nothing in the text, the toolbar or the status bar says that a passage has any or how many. That is a gap, not an unknown (see below).

What the review changed: the comment buttons (Reply, Resolve, Delete, Show in the text) said only the verb, 40 times over in a long review; each now carries the comment's author and time as its description. The field-definition list had fourteen buttons called "Save", each now a group named for its field; the Connect page's four "Copy" buttons carry the name of the snippet they copy. The four search forms were landmarks with no name. The dialog the whole application loads fragments into was labelled by a heading that did not exist until it was filled. The account menu opened with focus on Sign out, the last item; it now opens on the first. The command palette's and the chapter jump's fields had no focus ring. The inspector's section headings were level 3 straight under the page's level 1; they are level 2. Page titles that read "Goals The Salt Road" now read "Goals · The Salt Road".

What it did not find, because only a person with a reader would: whether "Chapter 1, 230 words" is the right amount of information for a tree row; whether the toolbar's roving stop is announced usefully; how the alternatives' marks, the ghosted text and the stash are voiced inside the editing surface; whether the writing partner's proposal, which is a dialog over the page, reads as a diff or as noise; whether the save state is too quiet or too chatty. These are on the checklist.

## Before a release: the manual checklist (NOT RUN)

Nothing below has been done. Each is a pass of the same script of tasks on a real book (sign in, open a chapter, write a paragraph, add and resolve a comment, restructure the outline, compile a proof, share a link, ask the writing partner and accept a change), with the equipment named. Tick a line only after doing it, and put the date and who did it beside it.

- [ ] VoiceOver on Safari, macOS: the whole task script; landmarks rotor, headings rotor, the tree, the toolbar, the live announcements, the comment margin, the alternatives marks, the proposal dialog.
- [ ] VoiceOver on Safari, iOS, at 390 px: the same, with the bottom tab bar, the formatting bar above the keyboard and the inspector as a sheet.
- [ ] NVDA on Firefox, Windows: the same, in browse mode and in focus mode inside the editing surface.
- [ ] NVDA or JAWS on Chrome, Windows: spot checks (forms, dialogs, the tree).
- [ ] TalkBack on Chrome, Android: the same task script at phone width.
- [ ] Narrator on Edge: sign-in, library, one chapter.
- [ ] Browser zoom to 200% and 400% at 1280 px on Chrome, Firefox and Safari: no loss of function, no sideways scroll except in code and tables.
- [ ] Firefox "Zoom text only" at 200%, and the operating system's larger-text setting (iOS Dynamic Type, Android font scale): the app's type is set in px, so the system setting is expected to do nothing; see Known gaps.
- [ ] Windows high contrast (Contrast themes, Aquatic and Desert at least) on Edge and Firefox: every page family; the focus ring, the selected tree row, the current tab, a pressed toggle, the save state, the comment marks and the diff in a proposal are all visible.
- [ ] Keyboard only, with the pointer unplugged, on the task script; also Switch Control or Voice Control (macOS or iOS) naming a few buttons by their visible words.
- [ ] Reduced motion set in the operating system and set in Settings, then each of Settings' other options (density, the editor's type size and face): nothing moves, nothing reflows while typing.
- [ ] A person who uses assistive technology every day, paid for their time, reads the pages and the product's copy and tells us what is wrong.

## Known gaps

Things we know do not meet the standard or are unproven, with the reason. The ones in the script's `ALLOW` list say so.

- **No assistive technology was run** (above). Everything here is the machine's word.
- **The breadcrumb ellipsises at large text.** At 200% text size, and under the text-spacing override, the project and page names in the header's breadcrumb are cut with an ellipsis, because the header is one fixed-height row and the page tabs now scroll inside their own bar instead of pushing the page sideways. The same names are the page's `h1`, the chapter jump button's accessible name and the title of the tab, so nothing is lost; the visible cut is. (Allowed in the script.)
- **The command palette's and the chapter jump's search fields have no box** (WCAG 1.4.11): a bare input beside an icon, set apart by the dialog's own rule, with its focus ring on the input. (Allowed in the script.)
- **The document's title is a heading with no box** (WCAG 1.4.11) and not 44 px tall on a phone. It is the page's `h1` edited in place; a box would make the title read as a form field and a taller one would push the text down. It shows its focus ring when edited. (Allowed in the script.)
- **Alternatives, ghosted text and the stash have no cue in the tree** of the editing surface. A reader can open the Alternatives tab and read the list, but is not told while reading that a word has alternatives, nor how many a chapter has. A count on the Alternatives tab and in the status bar would be the cheapest fix; it needs a decision on how chatty the writing surface may be.
- **The writing surface shows a caret, not a focus ring,** in ordinary colours, because the writing surface is not to be decorated. In forced colours it has an outline.
- **Type is set in px.** Browser zoom works and is what the audit's reflow checks stand for. A browser's or operating system's larger-default-text setting does nothing to the application's own text, and a text-only zoom (Firefox) is emulated here, not tested in Firefox.
- **The writing partner's own words are not in the message catalogue** (carried forward from Phase 4). This does not affect names or structure; it means those screens cannot be translated.
- **Right-to-left** layout of the application is scaffolded and not audited; no right-to-left manuscript is supported for print.
- **Dragging** (the manuscript tree, the outline, the corkboard) has keyboard equivalents, whose keyboard form (Alt+ArrowUp and Alt+ArrowDown) `e2e-outline.mjs` and `e2e-boards.mjs` exercise; the audit did not exercise a drag.
- **Exports** (EPUB, PDF, Word, the static site) are not audited for accessibility: the PDF is not tagged; the print PDF says so in FEATURES.
- **Pages that need a flow to reach** were not walked (see above). The 200% text and spacing checks examine pages as first loaded.
- **Cognitive and wording criteria** (plain language, consistent help, redundant entry, error suggestions) are partly designed in (DESIGN.md "Copy and tone") and not audited.
- **Sign-in** is the identity provider's page: its accessibility is its own.

## Reporting a problem

Open an issue on the project's tracker with: the page's address (without any token), what you were doing, what you expected, what happened, and the assistive technology, browser and operating system with their versions. For a share link, give the page and not the link. If you cannot use the tracker, tell whoever runs your instance. A fix that is made comes with a line in the audit (a check that fails before and passes after) when a script can see it, and in the checklist above when it cannot.
