# Editor spike (Phase 0)

The Phase 0 spike: a ProseMirror schema, a Markdown parser and serializer, the
round-trip test that exists before the editor does, and a mounted editor with
paste, IME, Source-mode, and performance checks. This page records what was built,
what was decided and why, the measured numbers, and what is not verified.

Everything here is factual as of the spike. Where a number comes from a
particular machine or browser, it says so.

## What exists

| Path | What |
| --- | --- |
| `web/editor/src/schema.js` | The ProseMirror schema. Pure data. |
| `web/editor/src/md-plugins.js` | markdown-it configuration: wiki links, private notes, footnotes, task items, source-block capture. |
| `web/editor/src/markdown.js` | `parse`, `parseDetailed`, `serialize`, `normalize`. Pure functions, no DOM, no View. |
| `web/editor/src/count.js` | `countWords` (private notes, code and source blocks excluded). |
| `web/editor/src/rules.js` | Input rules, commands, keymap. |
| `web/editor/src/paste.js` | HTML sanitising, paste-as-Markdown, copy as Markdown. |
| `web/editor/src/editor.js` | `mount()`, Source mode, debounce, dirty flag, limits, decoration hook. |
| `web/editor/src/index.js` | Bundle entry; defines `window.scriobhEditor`. |
| `web/editor/build.mjs` | esbuild: minified IIFE, ES2020, no source map. |
| `web/static/js/editor.js` | The built bundle (committed). |
| `web/static/css/editor.css` | The editing surface. Tokens only. |
| `web/editor/roundtrip.mjs`, `props.mjs` | Node drivers the Go test runs. |
| `web/editor/test/`, `web/editor/browser-test/` | Node tests and Chromium tests. |
| `internal/editor/` | `roundtrip_test.go`, `testdata/corpus/` (156 inputs, 52 goldens). |

Run (from `web/editor`, after `npm ci`):

```
npm run build          # rewrite web/static/js/editor.js
npm test               # Node: corpus, properties, focused checks
npm run test:browser   # Chromium: typing, paste, IME, CSP, performance
go test ./internal/editor -count=1     # from the repo root
```

Node is a build-time and test-time dependency only. Nothing at runtime shells out
to it and nothing at runtime needs `node_modules`. Versions are pinned exactly in
`package.json`; `package-lock.json` is committed; `node_modules` is not.

## The schema

Nodes:

| Node | Markdown | Notes |
| --- | --- | --- |
| `paragraph`, `heading` (1–6), `blockquote`, `code_block` (`params`) | usual | |
| `bullet_list`, `ordered_list` (`order`), `list_item` (`checked`: null, true, false) | `-`, `1.`, `- [ ]` | `tight` is kept on the list. Task items are in the schema. |
| `scene_break` | `* * *` | Every thematic break spelling parses to it. |
| `footnote_def` (`label`) | `[^label]: text` | Written where it was, never moved to the end. Holds blocks. |
| `source_block` (`kind`) | verbatim | Everything the schema does not model. See below. |
| `image` | `![alt](src "title")` | |
| `hard_break` | `\` + newline | |
| `wiki_link` (`target`, `alias`) | `[[Title]]`, `[[Title\|alias]]` | Inline atom. |
| `footnote_ref` (`label`) | `[^label]` | Inline atom. |
| `source_inline` (`raw`) | verbatim | Inline HTML and the like. Inline atom. |

Marks, outermost first: `private_note` (`%%text%%`), `link`, `em`, `strong`,
`strike` (`~~`), `code` (innermost, non-escaping).

Private notes are a mark, not a node, so they stay editable and can contain other
marks. They have their own mark so that word counts and exports can drop them.

## Canonical Markdown style

One style, deterministic: the same document always produces the same bytes.

- `*emphasis*`, `**strong**`, `~~strike~~`, `` `code` `` (padded with a space when it contains a backtick or has a space at both ends).
- ATX headings (`#` to `######`), trailing `#` escaped when it is text. Setext input is rewritten.
- `-` bullets. A bullet list directly after another bullet list uses `*`, because two adjacent lists with the same marker re-parse as one.
- Ordered lists are numbered from their first number, with `.`; an ordered list directly after another uses `)`. `1. 1. 1.` is renumbered `1. 2. 3.`.
- `>` quotes, fenced code with backticks (a longer fence when the code contains one; `~~~` only when the info string contains a backtick). Indented code becomes fenced.
- Scene break: `* * *`.
- Hard break: a backslash at the end of the line (not two trailing spaces, which editors strip).
- Links `[text](dest "title")`; a destination with spaces, `<`, `>` or unbalanced parentheses is written `<...>`. Destinations are stored as written: no percent-encoding, no punycode. A link whose text equals its destination is written `<autolink>`.
- Wiki links `[[Title]]` and `[[Title|alias]]`; footnotes `[^label]` and `[^label]: text` with four-space continuation.
- Private notes `%%like this%%`.
- One blank line between blocks (inside a tight list, none between items). LF line endings. Exactly one final newline; an empty document is zero bytes.
- Soft line breaks inside a paragraph become a space. Empty paragraphs have no Markdown spelling and are not written.
- Escaping: `\ * ~ [ ] \`` always; `_` unless between word characters; `<` before a letter, `/`, `!` or `?`; `&` before an entity-shaped run; runs of `%%`; and at the start of a line (including after a hard break) `-`, `+`, `>`, `=`, `#`, `1.`, `1)`, `:::`. A `^` right after a link's `[` is escaped so `[^x](url)` stays a link.

Why `* * *` for scene breaks: `---` collides with front matter at the top of a
document and with a setext heading underline after a paragraph line; `***` is the
opening of `***bold italic***`; `* * *` is unambiguous, reads as a break in plain
text, and is the usual manuscript spelling. All of `---`, `***`, `___`, `- - -`
are read as scene breaks; only the canonical spelling is written.

## What survives as source

Anything the authoring schema does not model is kept as a `source_block` (a visible,
editable, monospace block labelled with its kind) and written back byte for byte,
without escaping, fences, or reflow. Container prefixes (`> `, list indentation)
are stripped on read and put back on write.

| `kind` | Matches |
| --- | --- |
| `html` | HTML blocks and comments. |
| `alts` | An HTML comment starting `<!-- scriobh:alts`. Round-trips untouched; it is a source block like any other, with a label the host can hide later. |
| `table` | GFM tables. |
| `front_matter` | `---` YAML `---` (or `...`) at the very first line, only when the first line inside looks like YAML. |
| `directive` | `:::name` ... `:::` containers, only when closed. |
| `reference` | Link reference definitions. Uses of the reference become inline links, the definition stays where it was, so nothing is lost and the result is idempotent. |
| `unparsed` | The whole-document fallback: if the parser throws, or a node cannot be placed (a silent drop), the entire input is held as one source block and `onWarning` is called. The corpus asserts it is never reached. |

Inline HTML (`<b>`, `<br>`, `<!-- x -->` inside a line) becomes a `source_inline`
atom, written back verbatim. An unclosed `:::` line, an unmatched `%%`, and the like
are ordinary text and are escaped when written.

Raw HTML is not an authoring construct. Pasted HTML is sanitised into schema nodes
(below) and the markup is discarded.

## Decisions

| Question | Decision | Why |
| --- | --- | --- |
| Tables | Preserved as `table` source blocks in Phase 0. | F-077 is Phase 2; a table editor is a feature of its own. They cannot be lost or reflowed. |
| Task lists | In the schema (`list_item.checked`). | F-062 puts a task button on the Phase 1 toolbar. |
| Reference links | Resolved to inline links; definitions kept as source. | The rich surface has no reference-link construct; nothing is lost. |
| Footnote placement | In place, not collected at the end. | markdown-it-footnote moves definitions and drops unreferenced ones. |
| Link destinations | Stored as written (`md.normalizeLink` is the identity). | markdown-it otherwise percent-encodes: `/caifé.md` would come back as `%C3%A9`. "Markdown is the truth". |
| Smart quotes and dashes | Off by default; `typography: true` turns on `"`/`'`/`...`/`--` rules (code exempt, including an unclosed backtick). | F-074 makes it a per-project switch (Phase 3). The characters are stored as typed. |
| `---` input rule | Converts at once. `***` and `* * *` convert on Enter. ```` ``` ```` plus Space or Enter opens a fence. | `***text***` starts with `***`; converting it while typing would eat bold-italic. |
| Plain-text paste | Literal. Never interpreted as Markdown, no auto-detection. | A writer pasting `# 1` or `*` from an email means those characters. `Mod+Alt+Shift+V` (or `editor.pasteMarkdown(text)`) pastes the clipboard text as Markdown. |
| Plain-text paste line structure | Each non-empty line becomes a paragraph (ProseMirror's default). | Word's plain text puts one newline between paragraphs; hard-wrapped email cannot be told apart. No text is lost either way. |
| Images that are not on this instance | Kept in the document and in the Markdown, but `toDOM` never gives them a `src`: the editor draws the alt text (`img.remote-image`, `data-src` holds the destination). Pasted HTML images with an absolute, protocol-relative or `data:` source are dropped; relative and root-relative ones are kept. | Invariant 7, by construction: under the application's header (`script-src 'self'; style-src 'self'`, no `img-src`) an `<img src="https://...">` would be fetched. A test attempts every request under that header and fails on any off-origin one. Phase 1 stores pasted images as attachments (F-076). Rendering remote images in the reading view is the host's decision (and its `img-src`). |
| Internal cut and paste | Our own clipboard HTML (marked `data-pm-slice` by ProseMirror) may carry `data-src`; in foreign HTML `data-src` (a lazy-loading attribute) is dropped. | Copying a paragraph that holds an off-instance image and pasting it elsewhere must not lose the image; foreign pages must not smuggle one in. |
| Link destinations | `javascript:`, `vbscript:`, `data:` (except images), `file:` are refused on both paths, after stripping the control characters and spaces a browser ignores inside a scheme. On the Markdown path `md.validateLink` is wrapped to do that; on the DOM path `safeHref`; `toDOM` also refuses to write an unsafe `href`. | With `normalizeLink` as the identity (above), `[x](java&Tab;script:alert(1))` reached the link mark as `java<TAB>script:`, which a browser reads as `javascript:`. markdown-it's own check only trims and lowercases. Both obfuscated forms are in the corpus. The refused link stays as literal text. |
| Copy | `text/plain` is the selection's Markdown; `text/html` is the rendered rich text. | Copying into Source mode (or another Markdown editor) keeps the syntax. |
| Untouched documents | `getMarkdown()` returns the original bytes while the document object is unchanged. `onChange` does not fire. | Opening a non-canonical file must not rewrite it, or create a spurious save and revision. The first real edit canonicalises. |
| Source mode | `Mod+E` swaps a `<textarea>` over the exact Markdown. Text typed there is what `getMarkdown()` returns until a rich edit. Switching back replaces the document in one undoable step; with no change in the textarea nothing is parsed. | Never loses text, never rewrites bytes behind the writer. |
| Selection across the toggle | Best effort: marker characters are placed in a scratch copy and the selection is mapped by Markdown offset one way and by text offset the other. | Exact for plain prose; may be off by a character or two next to escapes. The textarea is scrolled by proportion, not by line. |
| Dirty flag | `isDirty()` is true from the first edit until `markSaved(version)`. `onChange(markdown, {version})` is not a confirmed save. `setMarkdown` on a dirty editor, or mid-composition, returns `false` and does nothing unless `{force: true}`. | The editor buffer is the truth until the server confirms (docs/ARCHITECTURE.md "Autosave and conflicts"). |
| Limits | Warn at 15,000 words (`onLimit({level: "warn"})`), open read-only at 25,000 (`{level: "readonly"}`). Limits are counted without private notes, code and source blocks. | docs/ARCHITECTURE.md "Large manuscripts". Crossing 25,000 while typing only reports; it does not switch to read-only under the writer. |
| Read-only | `tabindex="0"` so the surface still takes keyboard focus. | |
| Footnote labels | Any non-space text without `[` or `]`. Numbering is not computed; the label is shown. | Keeps the Markdown the truth. Auto-numbering is a reading-view concern. |

## The editor

```js
const ed = scriobhEditor.mount(el, {
  markdown, readOnly, typography,
  onChange(markdown, {version}) {},   // 700 ms idle, immediately on blur / hide / Mod+S
  onLimit({level, words, limit}) {},
  onWarning({kind, error}) {},        // a whole-document fallback happened
  onLink({from, to, text}) {},        // Mod+K
})
ed.getMarkdown()  ed.setMarkdown(md, {force})  ed.focus()  ed.destroy()
ed.isDirty()  ed.markSaved(version)  ed.flush()
ed.toggleSource(force)  ed.mode()  ed.wordCount()  ed.pasteMarkdown(text)
ed.exec("bold" | "italic" | "strike" | "code" | "private" | "h1".."h3" | "paragraph" |
        "quote" | "bullet" | "ordered" | "task" | "scene_break" | "undo" | "redo")
ed.setDecorations(state => DecorationSet)   // scriobhEditor.Decoration / DecorationSet
ed.view                                     // the ProseMirror EditorView
scriobhEditor.markdown.{parse, parseDetailed, serialize, normalize, schema, countWords}
```

Keys: `Mod+B/I` bold and italic, `Mod+Shift+X` strike, `Mod+K` link (calls
`onLink`, or removes the link under the caret), `Mod+E` Source, `Mod+S` flush,
`Mod+Enter` scene break, `Mod+Alt+0..3` paragraph and headings, `Mod+Z`,
`Mod+Shift+Z`, `Mod+Y`, `Tab` and `Shift+Tab` in lists only (elsewhere Tab leaves the
editor, so the keyboard is not trapped), `Shift+Enter` hard break, `Backspace`
undoes an input rule, and after a wiki link turns it back into source text.

Input rules: `# ` to `###### `, `- `/`* `/`+ `, `1. `, `> `, ```` ```lang ````+Space,
`---`, `[ ] ` at the start of an item, `**bold**`, `__bold__`, `*em*`, `_em_`,
`~~strike~~`, `` `code` ``, `%%note%%`, `[[Title]]`, `[[Title|alias]]`, `[^1]`.

Nothing animates or reflows while typing: the stylesheet disables transitions and
animations inside the editor, there are no node views, and decorations are only
added by the host.

Decorations: `view.setProps({decorations})` (wrapped as `setDecorations`) layers
alternatives, ghosts, comments and Lab marks without touching the schema or the
Markdown. A Chromium test draws a wavy-underline decoration over a word, types
around it, and checks the Markdown is unchanged. The CSS reserves `.scriobh-alt` and
`.scriobh-ghost`.

## Paste, and what the CSP does to it

HTML paste goes through two steps. First `sanitizePastedHTML` rewrites the string:
removes comments, `script`, `style`, `head`, `iframe`, `object` and friends;
drops `on*` attributes; turns table cells into blocks. Then ProseMirror maps the
result onto the schema with the `parseDOM` rules; unknown elements are unwrapped,
`<a href>` and `<img src>` pass a destination check, spans become marks only for
bold, italic and strike.

**Finding: under the application's CSP the browser refuses style attributes even in
an inert document.** With `style-src 'self'` and no `'unsafe-inline'`, Chromium 151
applies none of the `style="..."` attributes in a pasted string, whether parsed by
`innerHTML` on a detached document, `DOMParser`, or `<template>`: `element.style`
stays empty and a `securitypolicyviolation` (`style-src-attr`, `inline`) event fires
for each one. Two consequences were observed and fixed:

1. ProseMirror's stock style rules (`font-weight`, `font-style`, `text-decoration`)
   cannot work. A Google Docs paste wraps everything in
   `<b style="font-weight:normal">`; with `element.style` empty, the wrapper reads as
   bold and the whole paste came out as `**...**`. Word and Google Docs carry bold,
   italic and strike only in styles.
2. Every pasted style attribute would raise a CSP violation report.

So the style attributes are read from the string, before any DOM parse, and turned
into a `data-sx` token list (`b`, `i`, `s`, `nb`, `ni`) that the `parseDOM` rules
read; the attributes themselves are removed. No pasted style ever reaches a DOM.

ProseMirror's own DOM (the view, decorations, node DOM) uses no `style=""`
attributes: it sets properties through the CSSOM, which `style-src 'self'` allows.
Verified in Chromium 151 under the header as the plan gives it
(`script-src 'self'; style-src 'self'`) and again under a stricter one
(`default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'none'; img-src 'self'; base-uri 'none'; form-action 'none'`):
typing, input rules, Source toggle, decorations, paste of Word, Google Docs and web
HTML, and copy produced no violation, no `style` attribute anywhere in the DOM, and
no request except the page, the bundle, the stylesheet and the document's own
instance-local images. Requests are collected with Playwright's `page.on("request")`,
so an attempt to reach another origin (even one that cannot resolve) is seen, and the
test document contains an absolute, a protocol-relative and a `data:` image. A control
in the same test sets a style attribute and confirms the listener sees the violation,
so the empty result is meaningful.

## Numbers

Bundle (`node build.mjs`, esbuild 0.28.2, minified IIFE, ES2020):

| | bytes |
| --- | --- |
| `web/static/js/editor.js` | 395,308 |
| gzip -9 | **135,746** (budget 180 KB) |
| `web/static/css/editor.css` | 8,513 (2,559 gzip) |

Largest inputs in the bundle: prosemirror-view 98 KB, prosemirror-model 45 KB,
prosemirror-transform 32 KB, markdown-it's `entities` tables 50 KB (decode 27 KB,
encode 23 KB), prosemirror-markdown 13 KB (it carries a default parser that is
constructed at load and unused), prosemirror-state 12 KB, prosemirror-commands 10 KB,
the five Scríobh modules about 35 KB together. Not done, available if the budget
tightens: alias `linkify-it` (about 7 KB, linkify is off) and the `entities` encoder
to stubs. Loading the bundle touches no DOM and makes no request.

Round trip (`go test ./internal/editor -count=1`): 156 corpus inputs (104 canonical,
52 with goldens), each run through both the source modules and the committed
bundle: 312 file checks plus the property test and the bundle-freshness check, all
passing. The bundle build was reproduced from `npm ci` on darwin-arm64 only; byte
equality on other platforms is untested (esbuild output is not guaranteed identical
across platforms). The corpus covers nested, loose and tight lists, renumbering, code in lists,
quotes with lists and code, emphasis edge cases, escaping at line start, hard breaks,
headings with trailing `#`, setext, links with titles and angle brackets, reference
links, autolinks, images, wiki links, every scene-break spelling, footnotes, private
notes, front matter, HTML blocks, the alts comment (including one whose JSON is full
of Markdown punctuation), tables, task lists, directives, CRLF and CR input, tabs,
Unicode (fadas, emoji, RTL, combining marks, CJK, NBSP and thin spaces), very long
lines, many paragraphs, and the empty document.

Property tests (`props.mjs`, seeded): 71,802 literal-text cases (punctuation-heavy
strings placed in a paragraph, second paragraph, heading, bullet, loose, ordered and
task item, quote, quote in list, footnote, after and between hard breaks, inside em,
strong, link text, private note, next to a private note) all satisfy
`parse(serialize(doc))` equals `doc`; 1,000 random-mark paragraphs reach a fixed point
and keep their characters. The property found real bugs during the spike (`[^x](url)`
turning a link into a footnote reference; code spans with edge spaces; nested equal
marks losing their formatting after the inner close) and they are fixed.

Chromium tests (`npm run test:browser`): 42 tests, all passing (editor behaviour and
CSP 21, paste 10, IME 9, DOM round trip 1, performance 1). The DOM round trip selects
all in each of the 156 corpus documents, copies, pastes the HTML into an empty editor,
and requires the same Markdown back; it found that an empty link destination was
dropped, now fixed, and it is what guards `checked`, `tight` and `start`. Chromium 151.0.7922.34 (Chrome for Testing,
Playwright cache revision 1234) driven by `playwright-core` 1.65.0 through
`executablePath`; 1.65 expects revision 1254, and nothing misbehaved with 1234.
Override with `SCRIOBH_CHROMIUM`. The tests skip locally and fail when `CI` is set if
there is no browser.

Keystroke latency, 20,055-word document (407 top-level blocks), real key events,
caret at the end of a paragraph near the middle, 300 keystrokes about 33 ms apart,
headless Chromium on an Apple-silicon Mac. Three measures per keystroke, because they
answer different questions:

- **editor work**: from the `input` event (captured before ProseMirror sees it) to the end of `view.updateState` for that keystroke, i.e. ProseMirror reading the DOM change, applying the transaction and updating the DOM. The direct cost of the editor.
- **handled**: from `keydown` to a `setTimeout(0)` queued from that key's `input` event. The test checks that every keystroke reached the document and that `updateState` ran after the input event, so the timeout cannot precede the insertion. It includes the browser's own input pipeline and timer clamping, which is why it does not shrink under CPU throttling.
- **painted**: `keydown` to a `setTimeout(0)` queued from the first `requestAnimationFrame` after it. Includes up to one 16.7 ms frame of headless scheduling.

Typical run (the other two of three runs within a millisecond of these):

| ms | p50 | p95 | max |
| --- | --- | --- | --- |
| editor work | 0.4 | 0.8 | 2.9 |
| handled | 4.3 | 8.6 | 12.4 |
| painted | 8.7 | 16.3 | 18.3 |
| editor work, CPU throttled 4x (150 keys) | 0.6 | 1.1 | 3.1 |
| handled, CPU throttled 4x | 3.9 | 8.8 | 9.8 |
| painted, CPU throttled 4x | 9.5 | 16.2 | 17.6 |

One of the three runs had a stall of about 400 ms across three consecutive
keystrokes (keystrokes 23 to 25: 395, 361, 328 ms of editor work, decreasing by the
key spacing as the queue drained); the same run's other 297 keystrokes matched the
table. The cause was not identified (nothing in the editor runs on a timer during
typing; it may be a garbage-collection pause or the machine), and it did not recur in
the two other runs. It is reported, not excluded.

The editor-level cost per keystroke is far under the 16 ms target at p95 (about 1 ms,
3 ms at the worst keystroke, and 1 ms with a 4x CPU slowdown). Programmatic dispatch
of the same edit takes under 0.2 ms at p95 unthrottled and 0.9 ms throttled. The
"handled" and "painted" figures are dominated by the browser's input pipeline and
frame timing and are upper bounds. Other timings on the same document: parse, build
and render about 10 ms, first layout about 5 ms, serialization at idle 5-11 ms (off
the keystroke path). 4x CPU throttling is a rough stand-in for a mid-range phone, not
a measurement of one; the first keystroke after load on a real phone is not measured
here.

## What is automated and what is not

Automated, in Chromium only:

- Typing with real key events, input rules, keymap, undo and redo, Source toggle and selection mapping, debounce timing, blur flush, dirty and `setMarkdown` refusal, size limits, read-only, decorations.
- Paste sanitising for Word-style, Google Docs-style and web-page HTML, dangerous markup, plain-text paste, paste as Markdown, URL over a selection, copy.
- Composition through the DevTools protocol (`Input.imeSetComposition` and `Input.insertText`), so the browser itself fires `compositionstart`, `compositionupdate`, `compositionend` and `beforeinput`: a Japanese-style sequence in plain, bold, heading, list and private-note text, at the start and end of a paragraph, a cancelled composition, `setMarkdown` refused mid-composition, no `onChange` until idle. Plus a hand-built event sequence that mutates the DOM as a browser does.
- CSP behaviour, request log, 390 px width without horizontal scroll.

Not verifiable here and not claimed. These need a real device:

- iOS Safari: autocorrect, predictive text, dictation, smart punctuation, the keyboard shortcut bar, `Mod` as Cmd on a hardware keyboard.
- Android Gboard: composition and the suggestion strip, glide typing, voice typing, autocorrect with replacement ranges.
- Long-press accents, emoji picker, and the macOS press-and-hold menu.
- Selection handles, magnifier, and tap-to-place caret in rich text and in the Source textarea.
- The virtual keyboard resizing the viewport; caret kept visible; the formatting bar above the keyboard (not built).
- Real OS clipboard paste (`Mod+V`, "Paste and Match Style"); the tests dispatch synthetic paste events.
- Safari and Firefox of any kind.

Manual checklist for a human with a phone (iOS Safari and Android Chrome):

1. Open a document with 3 paragraphs, a list and a heading. Tap into the middle of a word; the caret lands where tapped and the page does not jump.
2. Type a sentence with autocorrect on. A correction replaces the right word, once. Undo (shake or the key) undoes only that correction.
3. Dictate a sentence. It appears once at the caret, with no duplicated words, and the caret ends after it.
4. Type `# ` at the start of a line: a heading. Backspace: the `# ` comes back as text.
5. Type `**bold**` and `*em*`, then `"quotes"` and `it's` (typography off: straight quotes stay as typed; if the keyboard inserts curly ones, they are stored as typed).
6. Long-press `e` and pick `é`; long-press `-` for a dash. The character is inserted once.
7. Compose with a CJK or Korean keyboard if available: preedit text shows, commit inserts once, the caret stays after it, nothing lands in the wrong paragraph.
8. Select a word with the handles, apply bold from the formatting control, extend the selection with the handles. Selection and bold behave.
9. Paste text copied from a web page and from a word processor: no styling outside bold, italic, links; no blank NBSP paragraphs; plain text pastes literally.
10. Switch to Source (button) and back: the caret is near where it was; type in Source with autocorrect off and check nothing is auto-capitalised.
11. Rotate; open the keyboard; type 30 words: the caret stays visible, nothing animates.
12. Put the phone in airplane mode after the page has loaded; type; reload with the network cut: the editor still loads and the text is where it was left (host autosave behaviour).

## Known gaps and limitations

- **Emphasis next to punctuation inside a word cannot round trip.** CommonMark does not let `*` open before punctuation when preceded by a letter (`a*(b)*`, `b*~~x~~*`), so an emphasised run that begins or ends with punctuation directly against a word character comes back as literal asterisks. Whitespace-separated runs are fine. This is a property of the format, not of the serializer; the property test keeps marked runs separated by whitespace for that reason.
- Turning a code block into a paragraph or heading converts its lines to hard breaks (or spaces in a heading), because a paragraph holding a newline character would read back as a space. A source block cannot be turned into a paragraph or heading at all.
- Soft line breaks become spaces, so a hard-wrapped import is joined into long lines. Empty paragraphs are not representable and are not written. Indented code becomes fenced; setext becomes ATX; `*` and `+` bullets become `-`; HTML entities are decoded; NUL becomes U+FFFD (markdown-it). All of these are idempotent after the first pass and listed as goldens.
- Prose containing `[`, `]`, `*`, `~`, `` ` `` or a backslash is written with backslash escapes (`\[sic\]`). Correct and stable, less pretty than hand-written Markdown, and it also affects the mirror.
- A wiki-link target cannot contain `]`, `[` or a newline; `[[a|b|c]]` is target `a`, alias `b|c`.
- Footnote definitions that are not at block level (inside a list item without the right indentation) are text.
- `*` and `+` bullets, `1)` lists and tight/loose layout are normalised on the first rich edit, not before.
- `setMarkdown` resets undo history. A Source round trip is one undo step.
- Crossing 25,000 words while typing reports `onLimit` but does not make the editor read-only.
- Only Chromium was exercised. In read-only mode `Mod+E` on macOS did not reach the page in the headless Chromium used here (the cause was not investigated); the Source toggle is also an API call and a host button is expected.
- The formatting toolbar, find and replace, image drop and attachment, hover cards, and the reading view are not part of the spike. `exec()` exposes the commands a toolbar needs.
- The word count counts whitespace-delimited runs containing a letter or digit; CJK text without spaces counts as one word per run. Locale-aware counting is a later decision.

## Needs from the host page and the Go spine

- Call `editor.markSaved(version)` with the version `onChange` gave you once the server has stored that text. Until then the editor stays dirty and refuses `setMarkdown`; there is no timeout.
- Serve `editor.js` and `editor.css` only on document routes. Define the theme tokens the stylesheet reads (below); every `var()` has a fallback.
- The stylesheet reads only tokens defined by the app stylesheet (`web/static/css/app.css`): the palette (`--fg`, `--fg-2`, `--bg`, `--surface`, `--surface-2`, `--muted`, `--line`, `--accent`), `--sel`, `--note-wash`, `--font-prose`, `--font-mono`, `--prose` and `--lh-prose` (a font size and its line height), `--measure`, `--measure-source` and `--source-size`. It carries no colour fallbacks; the browser tests load the real tokens.
- Treat an off-instance image in Markdown as the host's decision when rendering outside the editor; the editor itself never requests one.
- CI needs `npm ci` in `web/editor` before `go test ./internal/editor` (see below). There is no `go.mod` on this branch; the Go tests assume the module `github.com/crueber/scriobh` at the repository root.
- The corpus directory carries its own `.editorconfig` and `.gitattributes` so editors and git leave the fixtures' bytes alone (CRLF, CR, no final newline).

## Notes for the plan

Surprises worth recording in docs/OPEN-QUESTIONS.md are listed in the spike report
that accompanied this branch; the ones that change a decision:

- The CSP finding above: the app's CSP makes `element.style` useless for paste, so
  Word and Google Docs formatting is read from the HTML string instead.
- F-078 lists a lone `#` as a scene break. In CommonMark that line is an empty H1. The
  editor reads it as an empty heading; the `#` spelling belongs to the manuscript
  export presets, not to the stored Markdown.
- The brief refers to docs/OPEN-QUESTIONS.md section 35 (Phase 0 decisions). This
  checkout ends at section 34. The Go test drives the committed bundle over the
  shared corpus and also the source modules, and fails rather than skips when `CI` is
  set.
- CI needs `npm ci` in `web/editor` before `go test ./internal/editor`; without
  `node_modules` the source-level run and the bundle-freshness check fail under `CI`.
  The bundle run needs only Node.


---

## Alternatives, ghosts and the stash: what an editor must do

Phase 3.5 built the server side (ops, Markdown, counts, exports, import, mirror). Nothing here changes the schema or the Markdown round trip; the editor bundle needs the work below. The reasons are in [OPEN-QUESTIONS](OPEN-QUESTIONS.md#phase-35-alternatives-decisions).

### What already works, with no editor change

- **The comment.** The last line of a document that carries alternatives, `<!-- scriobh:alts {...} -->`, is held as the `alts` source block and written back byte for byte (corpus: `alts-comment*.md`). Treat it as read-only state: never edit it, and keep it last. The Source view shows it; the rich view may hide the block.
- **Ghost tags.** A ghosted range is `<scriobh-ghost>text</scriobh-ghost>`: two `source_inline` atoms around ordinary inline content. They round-trip today (corpus: `ghost-*.md`, `n-ghost-soft-break.md`; the only normalisation is the editor's usual one, a soft line break inside the range becomes a space). Nothing in the schema or the Markdown serializer is needed.
- **Counting.** `src/count.js` skips the text between a ghost's tags, with the same pairing rule as the server: inside one text block the first opener and the next closer pair up; an opener met while one is open and a closer with none open do nothing; an opener left open ghosts nothing. `wordcount.golden.json` and `wordcount-extra/ghost-*.md` hold both sides to it. The committed bundle was rebuilt for this one change; rebuild it (`npm run build` in `web/editor`) after merging other editor work.

### The save contract

What `web/static/js/doc.js` does today, and what the editor's save path must change. (This replaces an earlier version of this section that described a poll `doc.js` does not have.)

**Today.**
- `pump()` sends `{base_revision: st.rev, tab_id, content?, title?}` and `saved(item, snap)` takes only the revision from the answer: `st.rev = item.revision`, and `st.serverMd = snap.md`, the text it *sent*, not the text the server stored.
- `serverMd` is what the editor believes the server holds. It is compared with the server's `content` in four places: `conflict()` (to tell a lost-reply save that landed, `cur.content === u.md` or `=== st.serverMd`, and a revision conflict that only changed details elsewhere, `reason === "revision" && cur.content === st.serverMd`, from a real conflict, which shows the "changed somewhere else" dialog), `takeOver()` (`cur.content !== st.serverMd` decides whether to `adopt`), and `keepMine()` (which sets `st.serverMd = cur.content` and then re-sends `want.md`).

**What goes wrong with alternatives.** The server may store a different last line than the one the editor sent: it rewrites the alternatives comment when the context around an anchor changed, when an anchor was orphaned or adopted an edit made in place, or when the sent copy was an older one. After that `serverMd` is not what is stored, so a later revision conflict that only changed details fails the `cur.content === st.serverMd` test and shows a false "changed somewhere else" dialog, and `keepMine()` re-sends a comment line that is older than the one it just fetched. The server no longer loses anything when that happens (an older, unedited copy of the comment is ignored and the response says so in `notices`), but the dialogs are wrong.

**What the save path must change.**
1. In `saved(item, snap)`, set `st.serverMd` to the stored text, `item.content`, not to `snap.md`. If `item.content` differs from what was sent (it can only be the last line, the comment), swap the editor's `alts` block for the stored last line without touching the rest (a new editor call, for instance `ed.setAltsComment(line)`, which replaces the one `alts` source node and does not reset the selection or the history), and make `markSaved` for the version that was sent. Never replace the body from a save response.
2. Compare texts without their comment where `doc.js` compares `content` with `serverMd` or with a sent text (`conflict()` twice, `takeOver()`, `textPending()`): use a helper that drops a last line that starts `<!-- scriobh:alts `, on both sides, so a rewritten context line is never a reason to call something a change.
3. In `keepMine()` and `takeOver()` (after `adopt`), when `cur.content` is taken as the base, also take `cur`'s last line into the local text before anything is re-sent.
4. Show `item.notices` (the response of `doc.update`) as a quiet message when it is not empty.
5. Every alternatives op returns `revision`, `content_hash`, `word_count` and `comment` (the new last line, `""` when the document has no alternatives): set `st.rev`, swap the block for `comment`, and if the editor also applied the body change itself (cycling), set `st.serverMd` to its own serialization, which must equal the stored text; if it does not, ask for `with_content: true` and call `setMarkdown` with the result. Never send a save with the old revision.

Other points of the contract:
- A document that carries alternatives is stored with LF line endings only.
- Flush the pending save (`Mod+S` path) before calling an op: ops take the revision the text was saved at, and a stale one is a 409 with `current_revision`. Lock the editor for the length of an op, as restore does. Send `tab_id` so the single-writer lease applies.

### Naming text: Markdown source, not rendered text

The server anchors to the **Markdown source** of the body, and a rich-text selection is not a source offset. Send the source of the selection and of its surroundings:

- `quote`: what `serializeMarkdown` writes for the selected slice (a selection that spans `*emphasis*` includes the asterisks, a wiki link its brackets, a soft break the space the serializer writes).
- `before` and `after`: up to about 24 characters of source before and after, serialised the same way (a slice of the document around the selection).
- `occurrence`: only when the same quote with the same context occurs more than once.

The server answers 422 "occurs N times" when it cannot tell; widen the context and retry. Offsets (`start`, `end` in UTF-16 units of the body) are for clients that hold the source, such as the Source view (its textarea offsets are exactly these, after turning CRLF into LF) and the CLI.

Scope, on the editor's side: a **word** is a run with no whitespace (expand a caret or a partial selection to word edges, with `Intl.Segmenter` where available); a **sentence** lies in one paragraph or heading (the server does not segment sentences; the editor chooses the range); a **paragraph** is exactly one top-level paragraph or heading's text, from its first character to its last. Not allowed, and refused with a 422: a range across blocks, in code, a table, raw HTML or front matter, one that starts or ends inside a word, one that holds a ghost tag, one that overlaps another alternative (409). Alternatives do not nest.

### Drawing

All of it is `view.setProps({decorations})`; nothing enters the schema or the stored Markdown, and nothing animates or reflows while typing.

- **Where.** `alternative.list` gives each item's `start`/`end` (UTF-16 units of the body), `quote` and `before`/`after`. To turn those into ProseMirror positions, serialise the document once per change with a position-recording serializer (the Source toggle already builds a Markdown-offset map with marker characters; make it return the map) and map `start`/`end` through it. Fall back to searching the block text for `quote` with `before`/`after` if a position cannot be mapped. `status: "orphaned"` items have no place: list them in the panel as lost anchors, offer `alternative.reanchor` with the current selection, and draw nothing.
- **Marks.** `.scriobh-alt` on the anchored range: the hand-drawn wavy underline; end dots at its edges; the highlighted middle dot when `chosen_position` is not the original's (`variants[i].original`). Use `source` (`manual`, later `generated`) to tell a written wording's marker from a generated one's.
- **Ghosts.** For each pair of ghost atoms (same pairing rule as `count.js`), add `.scriobh-ghost` (about 10% opacity, still readable) over the content between them and hide the two atoms. `ghost.list` returns the same ranges with ids, who and when, and `raw_word_count` for the "with ghosted words" figure; the status bar's count already excludes them.

### Cycling in place

`alternative.choose` takes `variant_id` or `step` (`next`, `previous`, `original`; it wraps). Its response says what changed: `replaced` = `{start, end, new_end, text}` in body offsets, `text` being the new wording's Markdown source. Apply it to the document as one ProseMirror transaction that replaces the decorated range with the inline-parsed `text`, keeping the selection where it was, then swap the `alts` block (see the save contract). Queue rapid presses: each request carries the revision the previous response returned. Undo is another choice; do not use the editor's undo history for it (the server's history is the record). `alternative.add_variant`, `edit_variant` (editing the chosen wording changes the body too, and returns `replaced`), `remove_variant` (removing the chosen one puts the original back, also `replaced`), `reorder_variant` and `delete` (resolve) follow the same contract. Undoing a removal is `add_variant` with `after_variant_id`.

Automatic a/an, the audio cue and the delete animation are the editor's alone; `prefers-reduced-motion` and muted devices are respected there.

### Ghosting from the context menu

For a selection, send one range per text block it touches to `ghost.create` (up to 50). The server wraps each in the tags and answers with the new `ghosts`. Revive with `ghost.revive` and the ghost's id, or with the selection's range (every ghost it overlaps), or `all`. It refuses a range that starts or ends inside an alternative (409): ghost all of it or none.

### The stash

`stash.*` need the editor role; hide the panel for readers and reviewers. `stash.create` takes `title`, `content` (Markdown), `kind`, `tags` and `source_document_id`; "stash this selection" is `stash.create` with the selection's source, then deleting the selection with an ordinary edit. `stash.list` pages and searches (`q`); `stash.update` takes the revision. To put an item back, `stash.drag_back` with the document, its saved revision, `at` (a body offset: use the position map, or omit it for the end) and `placement` `block` (its own paragraph, the default) or `inline`; `mode: "move"` trashes the item. It is an edit of the document, so flush the pending save first and reload the document from the response (`with_content: true`). Nothing is ever inserted unless the writer asks.

### Corpus and tests

New corpus files (`internal/editor/testdata/corpus/ghost-*.md`, `n-ghost-soft-break.*`, `wordcount-extra/ghost-*.md`) pin the ghost round trip and count. When the decorations land, add a Chromium test next to the existing decoration test: a ghost and an alternative drawn over text, typing around them, and the Markdown unchanged.
