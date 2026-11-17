# Design

The app should feel like a well-made book and a quiet room, not a productivity dashboard.

Three rules decide most questions:

1. **The page is paper.** Flat fills, hairline rules, generous margins, one accent. No gradients, no glass, no drop-shadow cards floating over a tinted mesh.
2. **Type is the interface.** If a control can be a word, it is a word. If a heading can be separated by space, it does not need a box.
3. **Motion is a courtesy, not a performance.** 120–180 ms, ease-out, and nothing moves while the writer is typing.

---

## Chosen direction

**A. Paper** is the baseline, chosen from the wireframe review:

- Warm paper, warm ink, and a single **moss** accent (`#2F5D50`) used for the active chapter, links, and editorial marks.
- A 252 px manuscript tree, a 648 px paper page, and a 316 px inspector for scene metadata, alternatives, comments, and AI. The inspector is a tool, not a permanent dashboard.
- The library is a **shelf of covers**: flat typographic covers, a continue-writing card, and progress. No editorial index.
- Prose sits at 19/31 with paragraph spacing; no drop cap and no first-line indents in the editing surface. Those remain an option in the reading view, not the default.
- AI lives in the inspector's AI tab and a review bar; it is second-class and never in the writing surface.
- Mobile keeps the paper page, a clear five-item bottom bar, and a formatting bar above the keyboard.

The earlier directions (Desk, Nocturne, Folio, Quarto) are archived in git history. Paper is the only active direction; the compile dialog now lives in its wireframe.

---

## Typography

| Role | Face | Fallback | Why |
| --- | --- | --- | --- |
| Prose, editor, reading view | **Newsreader** (OFL) | Georgia, serif | The face the chosen direction was drawn with; warm, high-contrast, comfortable for long reading |
| Interface | **Instrument Sans** (OFL) | system-ui, sans-serif | Neutral, slightly bookish, excellent at 12–15 px; does not compete with the serif |
| Code, Markdown source, counts | **IBM Plex Mono** (OFL) | ui-monospace, monospace | The source is code-shaped; mono keeps the syntax honest |
| Optional dyslexia-friendly | **OpenDyslexic** (OFL) | — | Opt-in, per user |

The first three are self-hosted, subset to Latin + the punctuation we use, served as WOFF2 with `font-display: swap` and a preload for the prose face only. OpenDyslexic ships as an optional face. No Google Fonts. No CDN. Courier Prime is used in the wireframe specimens only and is not shipped.

As built (Phase 0): Newsreader and Instrument Sans are subset to Latin, Latin-1, Latin Extended-A and general punctuation with all layout features and all variable axes kept (159 KB and 80 KB). IBM Plex Mono is shipped unmodified, because its licence reserves the name "Plex" for unmodified copies. Four WOFF2 files, 317 KiB in all. Pinning Instrument Sans to its normal width (40 KB) and limiting Newsreader's weight to 300–700 (152 KB) is possible if the budget ever needs it.

### Scale

| Token | Size / line-height | Use |
| --- | --- | --- |
| `--text-xs` | 12 / 16 | counts, metadata, timestamps |
| `--text-sm` | 13 / 18 | secondary UI, tree rows |
| `--text-base` | 15 / 22 | UI default, buttons, labels |
| `--text-lg` | 17 / 26 | dialog titles, section heads |
| `--text-xl` | 20 / 28 | page titles |
| `--text-2xl` | 26 / 34 | library project titles |
| `--text-3xl` | 34 / 40 | empty-state headlines |
| `--prose` | 19 / 31 (user-adjustable 16–24) | editor and reading view |
| `--prose-sm` | 17 / 29 | reading view at narrow widths |

Editor measures: default `66ch`, adjustable 52–82ch, or full width. Line height 1.65 default (1.4–1.9). Paragraphs use a 0.95em gap with no first-line indent. The drop cap and book indents are reading-view options, not the editing default. A project's owner or an editor may choose a first-line indent for the project (F-085, custom typography), and each person may override it; it is then drawn in the writing view too, because a person who asked for it asked for it where they write. Nothing is indented until someone chooses it. Justification stays off by default; when on, `hyphens: auto` with the document language.

Numbers in stats use tabular figures. Fractions and quotes use proper glyphs. No letter-spacing on body text; 0.01em on small caps only.

---

## Colour

Six themes, each with one accent. Tokens only; no colour literal in a component. Accents are named for materials and landscapes, which are easier to talk about than hex values.

### Paper (default light)

```
--bg        #FBFAF7   warm paper, not white
--surface   #FFFFFF   editor page, dialogs
--surface-2 #F4F2EC   rails, inset panels
--fg        #1C1B18
--fg-2      #4A4740
--muted     #6F6A60
--line      #E2DED5
--accent    #2F5D50   moss
--on-accent #FFFFFF
--danger    #A03A2C
--warn      #8A6420
--ok        #3E6B45
```

### Ink (default dark)

```
--bg        #131311   near-black, warm, never #000
--surface   #1B1B18
--surface-2 #232320
--fg        #EDEAE3
--fg-2      #C4C0B6
--muted     #969187
--line      #2E2D28
--accent    #7FB09A
--on-accent #10201A
--danger    #D98A7C
--warn      #D6B26A
--ok        #8FBF95
```

### Others

| Theme | Background | Accent | Character |
| --- | --- | --- | --- |
| Slate | `#F7F8F9` / `#15171A` | `#3B5B7A` | cool, for daylight screens |
| Sepia | `#F4ECDD` / `#1C1710` | `#7A4B2A` | old paper, low blue |
| Moss | `#F5F7F2` / `#12150F` | `#4B6B3A` | green-tinted, outdoors |
| High contrast | `#FFFFFF` / `#000000` | `#0050A0` / `#7FB4FF` | WCAG AAA body text |

Paper `--muted` was darkened from `#7A756B` to `#726D63` because the original measured 4.39:1 on `--bg` and 4.09:1 on `--surface-2`, below the 4.5:1 rule. It was darkened again, to `#6F6A60` (and Ink's lightened from `#8E897E` to `#969187`, Sepia's from `#6E5E45` to `#6B5B43`, Moss's from `#8D9889` to `#939D8F`), because muted counts and dates on the accent wash of a current row measured 4.12 to 4.40:1; `check-contrast.py` now requires 4.5:1 for that pair too.

Selection, comment highlight, suggestion insertion, and search match each get a token, not an ad-hoc colour. Comment highlights are a 1 px underline plus a faint wash; they must never make prose harder to read.

**Contrast rule:** `--fg` on `--bg` ≥ 7:1; `--fg-2` ≥ 4.5:1; `--muted` ≥ 4.5:1 on every surface where it carries meaning, the accent wash of a current row included (not for decoration only), and the edge of a field (`--field-line`, which is `--muted`) is at least 3:1 against the page. `scripts/check-contrast.py` runs in CI over all themes and accents and fails the build.

No gradients anywhere, including the logo, covers, empty-state art, and skeleton loaders.

---

## Layout

### Desktop (≥ 1024 px)

```
┌────────────────────────────────────────────────────────────────┐
│ 52  Scríobh · project ▾      saved · 42,318 words   Ask  Ctrl K │
├──────┬──────────────────────────────────────┬──────────────────┤
│ tree │ editor: rich text at ~66ch           │ inspector 316    │
│ 252  │ chapter · title · body               │ scene            │
│      │                                      │ alternatives     │
│      │                                      │ AI · comments    │
├──────┴──────────────────────────────────────┴──────────────────┤
│ status: ch 12 · 1,840 words · Source Ctrl+E · focus · 100%     │
└────────────────────────────────────────────────────────────────┘
```

- The tree collapses entirely (`Mod+\`) for focus; the editor page is the only thing left.
- The inspector is 316 px and shows one tab at a time: Scene, Alternatives, AI, Comments. Below 1150 px it becomes a slide-over sheet, not a squeezed column.
- The AI review bar sits above the status line, never over text. "Accept all suggestions" is a single action with a preview and one revision.
- The header is 52 px and contains no navigation the palette cannot do. The library uses the full width and has no tree.
- The tree row carries the chapter number, title, and count; the current chapter gets a 2 px accent bar and a faint accent wash.

### Tablet and phone

```
┌─────────────────────┐        ┌─────────────────────┐
│ ‹ Chapter 12   ⋯    │        │  Library            │
│                     │        │  ┌───┐ ┌───┐        │
│  prose, 20px        │        │  │   │ │   │        │
│  gutters            │        │  └───┘ └───┘        │
│                     │        │                     │
├─────────────────────┤        ├─────────────────────┤
│ B I H  “  •  1.  ⋯  │ sticky │ Write  Plan  Search │
├─────────────────────┤        │        More         │
│ Write Plan Search + │        └─────────────────────┘
└─────────────────────┘
```

- A 56 px bottom bar with five destinations: Library, Write, Plan, Search, More. Each has an icon and a word label, 44 px targets, `padding-bottom: env(safe-area-inset-bottom)`.
- The formatting bar sits directly above the keyboard, horizontally scrollable, 44 px targets, no labels on the smallest screens.
- The tree is a full-height drawer with a search field at the top.
- Chapter switching is a bottom sheet, not a dropdown that opens off-screen.
- The chapter head carries POV and word count above the title. Title and body are one continuous scroll; the title is a large text field above the rich body.

### Density

Three densities, one default: comfortable. The others are for laptops and for people who like more on screen. Density changes spacing, never type size.

---

## The editor

The editor is a rich surface over a Markdown document, not a textarea.

- The rich surface parses Markdown on open and serializes it on every save. The stored text is always Markdown.
- `Mod+E` switches to **Source**: the exact Markdown in IBM Plex Mono with syntax highlighting, editable in place. On wide screens a split Source/rich view is available.
- Every rich construct has a Markdown representation. Unknown Markdown falls back to a visible source block and is never dropped. A golden round-trip test runs in CI.
- The caret is the browser's caret inside the rich surface. Mobile IME, paste, undo, and selection are acceptance tests, not assumptions.
- Headings, emphasis, links, wiki links, lists, quotes, code, and images render in place. Wiki links get a dotted underline and a hover card.
- In focus mode, paragraphs other than the active one dim to 55%, never below 4.5:1 effective contrast for the active paragraph.
- Typewriter scrolling is opt-in and respects `prefers-reduced-motion`.
- The save state lives in the header as one word: `Saved`, `Saving…`, `Offline`, `Conflict`. No spinner for a 200 ms save.
- Word count updates in the status bar, never inline near the caret. Ghosted words are excluded.
- The AI suggestion is an absolutely-positioned decoration with `aria-hidden`; the real insertion happens only through an explicit accept.

**Reading view** is the same Markdown rendered as a book page: title, optional drop cap, scene-break ornament, footnotes at the bottom, images with captions. It is a reading surface, not a second editor, and it is what "Preview" meant in the old plan.

**One decoration slot.** The editor draws a single set of decorations. Anything that marks the text without changing it (focus dimming, wiki-link marks, later alternatives, ghosting and comments) registers through `scriobh.decorate(editor, name, fn)` in the shell, where `fn(state)` returns an array of decorations, and the slot draws the union of every registered function. Calling `editor.setDecorations` directly replaces the others and is a bug. Registering a name again redraws, which is how a function announces that its answer changed.

### Markdown fidelity

Rich text is a view, and views leak. The rules that keep the file honest:

- The schema is CommonMark + GFM, plus wiki links, scene breaks, footnotes, and private `%%notes%%`.
- The serializer is deterministic: the same document produces the same bytes.
- Raw HTML is not part of the authoring schema. Pasted HTML is sanitised into schema nodes, never stored as markup.
- Anything the parser does not understand survives as text, so a stray directive cannot vanish.
- The Source view is always available, even when the rich surface has a bug. It is the escape hatch that makes the rich editor acceptable.

### Alternatives, ghosting, and the Lab

The editor has to show what is provisional without making the page look like a diff.

- **Alternatives** get a 1 px wavy underline in the accent at 70% strength, plus two 2 px dots at the end of the range. No pill, no icon, no colour fill. The underline says "there is more here", not "this is wrong".
- **The middle dot** sits in the text at the end of the chosen variant: hollow on the original, filled on any other. It is 4 px, not a badge.
- **The side panel** is 300 px, opens on the right, tabs Word / Sentence / Paragraph, and lists variants with a bullet for the writer and a small bot glyph for the model. The original is pinned at the top, labelled, and cannot be deleted.
- **Ghosting** renders at 10% opacity. On hover it rises to 25% so it can be read; on focus it becomes fully legible until the cursor leaves. It is never announced as deleted, and ghosted words are excluded from the word count.
- **Lab marks** are a 2 px dotted rule under the range, in a muted amber, with the reason in the panel. Trim suggestions use ghosting, not strikethrough: the point is to feel the piece without the words, not to look at a redline.
- **Accept and reject** appear in a single bar at the bottom of the range, not in a modal. Accept applies in one revision; reject removes the mark.
- **Motion:** the delete explosion is 160 ms, 10–12 particles, accent-coloured, and never interrupts typing. With reduced motion it is a 120 ms fade. Audio cues are two short tones (660 Hz on switch, 440 Hz on return), opt-in, and suppressed while the device is muted.
- **Keyboard:** `Alt+↑/↓` cycles the alternative at the caret, `Alt+1/2/3` focuses Word / Sentence / Paragraph, `Alt+G` ghosts the selection, `Alt+R` revives. Every action also exists in the command palette.

The rule underneath all of this: **the page must still read as prose**, not as an editing console. If a mark makes the sentence harder to read, it is too loud.

---

## Components

| Component | Rules |
| --- | --- |
| Button | Three variants: solid (one per view), quiet (borderless), danger. Never icon-only without an `aria-label` and a tooltip |
| Input | 1 px line, no box shadow; focus ring is a 2 px accent outline with 2 px offset |
| Toolbar | Icon buttons in a single row, 32 px (desktop) / 44 px (touch), separators as 1 px vertical lines |
| Popover | Fixed-position, portaled to `#popover-root`, 6 px radius, one hairline border, no arrow cartoon. Never clipped by a scroller |
| Dialog | Native `<dialog>`, max 560 px, title, one primary action, Escape and backdrop close, focus trapped |
| Toast | Bottom-left desktop, above the bottom bar on mobile. One line, optional Undo, 6 s, stacking max 3 |
| Tree row | 28 px tall, indent guides as hairlines, count right-aligned in mono, drag handle appears on hover but is always reachable by keyboard |
| Card | Only for project tiles, scene cards, and research. 4 px radius, hairline, flat fill, no shadow |
| Diff | Word-level, insertions as a green wash with a left rule, deletions struck in muted red. A hunk header with Accept / Reject |
| Progress | A 2 px ring or a 3 px bar. Never a donut with a percentage in the middle on every card |
| Heatmap | 11 px squares, one hue, five steps, no red for missed days |
| Command palette | 640 px, centered, 44 px rows, fuzzy match with match ranges bolded, shortcut on the right |
| Empty state | One sentence, one action, no illustration unless it earns its bytes |

Icons: one inline SVG sprite, 1.5 px stroke, 20 px grid, round caps. Around 40 icons total. No icon font.

---

## Motion

| Token | Value | Use |
| --- | --- | --- |
| `--dur-1` | 120 ms | hover, focus, colour |
| `--dur-2` | 180 ms | popover, toast, panel slide |
| `--dur-3` | 240 ms | dialog, drawer |
| `--ease` | `cubic-bezier(.2,0,0,1)` | everything |

- Never animate text layout, width, or position while typing.
- No bounce, no spring, no parallax, no skeleton shimmer. A skeleton is a static grey block.
- `prefers-reduced-motion: reduce` sets all durations to 0.01 ms and disables transforms.
- Page transitions do not animate; the server sends HTML and the browser paints.

---

## Writing surfaces compared

| Surface | Content | Font | Measure |
| --- | --- | --- | --- |
| Rich | Rendered prose being edited | Newsreader, 19 px | 66ch |
| Source | Markdown with syntax highlighting, editable | Plex Mono, 15–17 px | 72ch |
| Focus | Rich or Source, chrome hidden | user choice | 62ch |
| Reading | Rendered prose, read-only, book-like | Newsreader | 62ch |

The user can set a different face and size for the rich and Source surfaces; the choice is remembered per device.

---

## Copy and tone

- Buttons are verbs: **Save**, **Export**, **Add chapter**. Not "Submit", not "OK".
- Empty states explain the next action in one sentence: "No chapters yet. Add one to begin."
- Errors state what happened and what to do: "Could not save. Your text is still here. Retrying in 3 seconds."
- No exclamation marks. No "Oops!". No celebration confetti. No emoji in system copy.
- Numbers are formatted with the user's locale but never abbreviated in the editor ("42,318", not "42k").
- Destructive confirmation names the thing: "Delete chapter 12 and its 14 versions?" with the action **Delete**, not **Yes**.

---

## Accessibility

The standard, the automated audit (`make a11y`), the manual release checklist (not yet run) and the known gaps are in [ACCESSIBILITY.md](ACCESSIBILITY.md); this section is the design rules it tests.

- Every interactive element is reachable and operable by keyboard, in a logical order, with a visible focus ring that is never removed without replacement.
- The tree is a real `role="tree"` with arrow-key navigation; drag-and-drop has a keyboard equivalent (Move up/down/into).
- Save state and word count are announced through a polite live region, debounced so a screen reader is not spammed.
- Comment anchors and diff hunks have text alternatives; colour is never the only signal.
- The editor's decoration layer is `aria-hidden`; the editing surface carries the accessible name and the document's language.
- The rich editor is fully keyboard-operable: every mark, insertion, and Source toggle has a command. If a screen reader struggles with the rich surface, the Source view is a real, editable fallback rather than a read-only curiosity.
- Zoom to 200% without horizontal scrolling on a 1280 px viewport.
- Target size 44×44 px on touch, 32×32 on pointer.
- The edge of a field is `--field-line`, at least 3:1 against the page; hairlines (`--line`) separate things and are never the only mark of a control. Placeholders are `--muted`.
- Forced colours: each state is also a shape. Where you are is underlined, what is chosen is outlined, the focus ring is an outline (a shadow is dropped by the system).
- Reduced motion, high contrast, and dyslexia-friendly font are settings, not media queries alone.
- Focus is restored after dialogs and popovers close, including after an htmx swap.

---

## What we take from quire, and what we do not

**Take:** the paper-like restraint, a Markdown Source view beside a typeset surface, focus mode with dimmed paragraphs, typewriter scrolling, a warm light theme and a true dark theme, the command palette as the primary navigation, editor font and measure as first-class settings, a calm settings page.

**Do not take:** the desktop-app window chrome, the file-tree-as-whole-mental-model, the "every note is a file" constraint as the only storage story, the Android-native app, the 14-theme carousel (six is enough; more is a maintenance tax).

See [RESEARCH.md](RESEARCH.md) for the full list of what came from where.
