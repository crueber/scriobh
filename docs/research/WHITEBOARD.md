# A whiteboard or planning board inside Scríobh: library research and spikes

Prepared 2026-11-22. Nothing in the repository was changed. Spikes live in
a throwaway spike folder outside the repository (not committed)
(npm used there only, as a build-time tool).

Provenance tags, as in `docs/research/WRITERS-BOARDS.md`:

- MEASURED: I built it or ran it here and read the number off the run (esbuild 0.28.2; Chrome 151 as Playwright's Chromium build 1234, driven by Playwright 1.62; Playwright's WebKit build 2336; all on macOS, Apple silicon).
- READ: I read the text (licence file, package source, docs page, issue, repository metadata) on 2026-11-22.
- SNIPPET: search-result text only. Do not rely on it.

---

## 1. Summary and recommendation

**Build a structured board first, by hand, in the house style. Do not adopt Excalidraw for it. If a freeform layer is wanted, make it a mode of the same board (SVG, attribute positions). Keep a freehand-drawing layer as a separate, later, evidence-driven decision, and price it before choosing a library.**

Why, in order of weight:

1. **What writers do on boards is a grid of cards, not a drawing.** WRITERS-BOARDS says one scene per card, columns as acts or POVs or subplots, colour for thread or status, and moving cards as the main act. Scríobh already holds the fields such a grid needs: `documents.parent_id` (part/act), `status`, `pov_entity_id`, `story_date`, `position REAL`, `synopsis_short`, and tag colour tokens (`slate, clay, ochre, moss, teal, blue, plum, rose`). A board is a view over data that exists, and a card is a real document. A drawing library has no notion of any of this.
2. **Every canvas candidate I tested fails the project's own tests in a specific, measurable way** (section 4). Excalidraw needs a patched build to avoid a CDN fallback, and even then its SVG export and image insert collide with the CSP. Elements are canvas pixels: in a 20-card scene the accessibility tree held zero named scene nodes, and 40 Tab presses reached only toolbar buttons. All 28 visible controls at 390 px were smaller than 44 x 44 px. The price is 445 KB gzip of JavaScript per session and 22.8 KB gzip of CSS, against a 180 KB limit for the editor and a 25 KB per-page CSS limit.
3. **A hand-rolled board passes everything the libraries fail.** The spike is 1.4 KB gzip of JS and 1.0 KB of CSS. It ran under the app's exact CSP with zero violations and zero `style` attributes, in Chromium and WebKit. Pointer drag of a card between cells, pointer drag of an SVG note (the transform attribute changed, checked in both engines at 1280 and 390 px), touch drag by the grip (emulated), and keyboard moves all worked, and the 40-card, 300-card and 1,500-card cases were smooth. The real thing will be bigger (the existing `boards.js` is 6.2 KB gzip, `outline.js` 7.2 KB), and still a tenth of Excalidraw.
4. **tldraw is out on licence alone.** Production use needs a licence key; the unlicensed SDK is development-only, a watermark is shown on the hobby tier, and its default build fetched 36 files from `cdn.tldraw.com` on load.
5. **The "both" answer is cheap if layered.** Structured grid (DOM, CSS grid) is the product. "Free layout" is the same cards with x and y and SVG `transform` attributes, which pass the strict CSP and the smoke test's `[style]` rule (measured). JSON Canvas (MIT, Obsidian's format) is a good projection format for it, and the importer currently skips `.canvas` files, so there is a natural tie-in. Freehand drawing (a world map, a family tree) has no documented writer use in the evidence report beyond mood boards and mind maps in a small questionnaire; if it arrives, the smallest option is an SVG pen layer built on `perfect-freehand` (2.0 KB gzip, MIT).

If the owner still wants Excalidraw, section 5.5 lists the conditions: a second pinned bundle (an amendment to invariant 10), a patched build, an exception to the smoke test, a CSP decision, and an honest statement that it is not accessible and not a card board.

---
## 2. Comparison table

All sizes are gzip -9 of minified output, MEASURED (esbuild, `NODE_ENV=production`, ES2020, ESM with splitting for libraries that split; "initial" is the entry plus every statically imported chunk). React baseline (react + react-dom/client, nothing else): 68,884 B. Existing Scríobh files for scale: `editor.js` 140,078 B (limit 180 KB), `boards.js` 6,190 B, `outline.js` 7,184 B, base `app.css` 9,222 B.

| Candidate (version) | Licence | JS gzip | CSS gzip | React? | Strict-CSP result (MEASURED unless marked) | Keyboard and screen reader | Verdict |
|---|---|---|---|---|---|---|---|
| Excalidraw 0.18.1 | MIT (READ) | 444,873 initial; 448,127 fetched in a draw-and-menu session; 2,632,228 across all 180 chunks | 22,761 | Yes (peer) | Default: 230 `font-src` violations to esm.sh. With `EXCALIDRAW_ASSET_PATH` set: still 230. With bundle patched: 0 on load, draw, text, menu. Driving SVG export: `eval` and `style-src-attr` violations, the font-subsetting worker timed out, glyph subsetting was skipped with an uncaught EvalError (MEASURED). Image insert not driven; it uses blob workers (READ), which the CSP blocks (probe, MEASURED). 16 to 31 `[style]` attributes | 0 named scene nodes of 20; Tab never reaches an element; 28 of 28 controls under 44 px at 390 px | Not for the board. Possible only as an isolated sketch type, with conditions (section 5.5) |
| tldraw 5.6.2 | tldraw licence: development only without a key; trial, hobby (watermark kept) or commercial keys (READ) | 679,361 | 14,607 | Yes | 36 requests to cdn.tldraw.com; 22 `img-src`, 16 `font-src`, 1 `style-src-elem` violations; 90 to 104 `[style]` plus one `<style>`; watermark present | Not tested further | Rejected on licence and privacy |
| @xyflow/react 13.0.2 | MIT (READ). The docs ask that the attribution badge be removed only with a Pro plan, $169 or $289 a month (READ) | 155,190 (of which React 68,884) | 2,736 | Yes | 0 violations, 0 off-origin requests, 0 console errors in Chromium and WebKit. `[style]`: 29 for 20 nodes, 309 for 300, 1,509 for 1,500 | Nodes are `role=group`, `tabindex=0`, no accessible name; arrow keys move a selected node; an assertive live region says "Moved selected node down. New position, x: 10, y: 20" with no title | Best of the React libraries; wrong model (free x,y, no lanes); still React and `[style]` |
| @xyflow/system 1.0.0 (drag and pan-zoom only) | MIT | 26,853 | n/a | No | Not run; READ only | You write the rendering | Only if a graph of links between cards is wanted later |
| Konva 10.10.0 | MIT (npm; GitHub shows no SPDX) | 59,621 | none | No | Size only; no `new Function`, no `eval`, no inline `<style>` (static grep, READ) | Canvas; no DOM per card; screen reader needs a parallel DOM | Not needed |
| Fabric.js 7.4.0 | MIT | 93,550 | none | No | Size only; sets `cssText` through CSSOM (READ) | Canvas | Not needed |
| Paper.js 0.17.0 | MIT | 126,979 | none | No | Size only; contains one `new Function` (static grep, READ), blocked by the CSP | Canvas | Not needed |
| JointJS @joint/core 4.3.4 | MPL-2.0 (file-level copyleft) | 142,983 | none | No | Size only | SVG, diagram oriented | Not needed |
| Drawflow 0.0.60 | MIT | 9,149 | 655 | No | Size only | Node graph, vanilla. Last release 2024-09-03, 274 open issues | Wrong model; unmaintained |
| SVG.js 3.2.8 | MIT | 29,757 | none | No | Size only | SVG helper | Not needed (SVG by hand is smaller) |
| SortableJS 1.15.7 | MIT | 12,794 (full build) | none | No | Size only; no eval, no `setAttribute("style")` | DOM lists; no keyboard moves of its own | Fine if a library is wanted for reorder; not needed |
| interact.js 1.10.28 | MIT | 29,701 | none | No | Size only | Gestures; none of the semantics | Not needed |
| Muuri 0.9.5 | MIT | 23,609 | none | No | Size only. Last release 2021-07-09, last push 2024-05-25 | Grid packing | Not needed (and idle) |
| Pragmatic drag and drop 4.0.0 (Atlassian) | Apache-2.0 (READ) | 6,956 (element adapter, my build); vendor says about 4.7 KB core | none | No | Size only | Uses the browser's own drag and drop; accessibility is left to you | A good small option if hand-rolled is ever judged too much |
| dragula 3.7.3 / @formkit/drag-and-drop 0.7.0 | MIT | 4,339 / 8,898 | none | No | Size only. dragula last push 2024-06 | Mouse-led | Not needed |
| perfect-freehand 1.2.3 | MIT | 1,973 | none | No | Size only | Output is an outline polygon for an SVG path | The pen for a freehand layer, if ever wanted |
| Hand-rolled DOM grid plus SVG layer (this spike) | n/a | 1,387 | 1,021 | No | 0 violations, 0 `[style]`, in Chromium at 1280 and 390 px and in WebKit | 121 focusable stops; Alt+arrows move cards and announce; arrows move SVG notes | Recommended |
| Penpot (latest release 2.19.3, docs only) | MPL-2.0 (READ, GitHub metadata) | n/a | n/a | n/a | Not tested | A full design application deployed with Docker Compose or Kubernetes; the pages I read name no embeddable component | Not an embeddable piece |
| draw.io (docs only) | Apache-2.0 (READ, GitHub metadata) | n/a | n/a | n/a | Not tested | Diagram editor meant to be embedded as an iframe | Out of scope |
| AFFiNE (docs only) | GitHub shows no SPDX; a search result says part of it is not MIT (SNIPPET) | n/a | n/a | n/a | Not tested | Full application | Out of scope |

---
## 3. Per-candidate notes

### 3.1 Excalidraw 0.18.1 (the owner's guess)

Fair credit first. It is MIT (READ: `package.json` and GitHub). Its repository is the most active in the field: pushed 2026-11-21, 136,090 stars, 364 contributors, 3,592 open issues (READ: GitHub API). The published package is a different story: **one stable npm release in the last 12 months** (0.18.1, 2026-04-20; 41 stable versions in all), alongside a stream of `0.18.0-<hash>` preview builds (READ: npm registry, versions matched with a strict `x.y.z` filter). Pinning therefore means pinning a seven-month-old stable build or building from a tag; the esm.sh patch below depends on that build's internal strings. The scene format is plain JSON, fonts can be self-hosted, it works in WebKit, and its toolbar adapts to 390 px (screenshot in `out/shot-excalidraw-390.png`). For brainstorming and sketching it is the nicest thing here. The problems are about fit, not quality.

**Licence and privacy.** MIT. No licence key, no watermark. The package contains hardcoded endpoints for collaboration, sharing, libraries and AI (`json.excalidraw.com`, `excalidraw-room-persistence.firebaseio.com`, `libraries.excalidraw.com`, `oss-ai.excalidraw.com`; READ by string search of the built chunks). Nothing fired in my sessions; they sit behind UI the host can hide with props, but they ship in the bundle and a privacy review will have to confirm that each is unreachable.

**Fonts and the CDN.** The default asset base is a CDN (docs say `esm.run`; the 0.18.1 code says `https://esm.sh/@excalidraw/excalidraw@0.18.1/dist/prod/`; READ both). Findings:

- Default load with no CSP: one attempted request to esm.sh for Excalifont at load (aborted by my router). MEASURED.
- Default load under the app's CSP: 230 `font-src` violations and 232 console errors, mostly the Chinese fallback font (Xiaolai) in subsets. MEASURED.
- With `window.EXCALIDRAW_ASSET_PATH` pointing at a same-origin folder (the documented way): the fonts that are needed load from there, **but the 230 violations remain.** Cause (READ, `chunk-K2UTITRG.js`): each `FontFace` is built with a `src` list that holds the asset path *and* `ASSETS_FALLBACK_URL` (esm.sh), and Chromium reports the CSP breach for every URL in the list at construction. `EXCALIDRAW_ASSET_PATH` therefore does not satisfy the smoke test.
- With the bundle patched at build time (an esbuild `onLoad` plugin that rewrites the string `https://esm.sh/` to `${location.origin}/ex-assets/`): zero violations, zero console errors, zero off-origin requests, in Chromium and in WebKit, for a session that draws a rectangle, types text and opens the main menu. MEASURED. A replacement that is not an absolute URL breaks the app (`Invalid base URL`), which I hit first.
- Open upstream issues confirm the shape of the problem: #7657 "Unable to use excalidraw in browser extension [CSP lvl. 3]" (open since 2024-02), #7672 asset path as env variable (READ).

**CSP beyond fonts** (MEASURED with the app's exact policy, plus READ of the bundle):

- Clicking SVG in the export dialog (MEASURED, with the server's real header on every response, scripts included) produced two violations: `script-src blocked=eval` from the font-subsetting chunk, and `style-src-attr blocked` from `setAttribute("style","white-space: pre;")` on exported text (7 such calls in the bundle). Excalidraw logged "Failed to use workers for subsetting, falling back to the main thread. Active worker did not respond for 1000ms", then "Skipped glyph subsetting EvalError ... 'unsafe-eval' is not an allowed source of script", and the page raised an uncaught EvalError. The save-file picker was then aborted by the headless browser, so no file was written; I did not check what an export would contain.
- The same CSP blocks `eval` and `new Function`, `WebAssembly.compile` (`wasm-eval`) and `blob:` workers; allows CSSOM writes (`el.style.x`), constructable stylesheets and SVG `transform` attributes (probe in `csp-probe.mjs`).
- Font subsetting for SVG export is a module worker with a wasm module whose glue calls `new Function` (READ); the run above shows it failing under this policy. The app's middleware (`securityHeaders` in `internal/server/middleware.go`, wrapping the whole mux) puts the same policy on `/static/` responses, so a worker script carries it too (READ; my server did the same). Image insertion goes through `pica`/`image-blob-reduce`, which create blob workers (READ). I did not drive image insert; the probe shows blob workers are blocked by `worker-src 'self'`.
- Net: draw, text, move and select ran without a violation (undo, library and image insert were not driven). SVG export with fonts failed in the run above, and image insert is expected to fail; both would need the CSP loosened (`wasm-unsafe-eval`, `unsafe-eval`, `blob:`, `style-src-attr`), which the project forbids.

**Size.** 7 files, 1,373,041 B raw, 444,873 B gzip initial; entry alone 802,002 / 249,628; React is 68,884 of that. CSS 144,577 B raw, 22,761 B gzip, containing two `linear-gradient` declarations (invariant 9; removable). All 180 chunks: 7.9 MB raw, 2.6 MB gzip (mermaid, cytoscape, harfbuzz and about 50 locales). The package ships 13 MB of fonts, 12 MB of them the Chinese fallback. Non-CJK fonts are 480 KB raw. Embedding an Excalidraw in the Go binary, pruned to the files a session fetched plus English and the non-CJK fonts, is roughly 2 MB raw by my estimate (not built); the binary has about 6 MB of headroom (34.07 of 40 MB).

**Data model.** One JSON scene: `elements[]` (rectangles, text, arrows, frames, freedraw) with absolute x, y, a fractional-index `index` and a version nonce, plus `appState` and `files`. `convertToExcalidrawElements` builds a labelled rectangle from `{type, x, y, label}`; elements can carry `customData` and `link`, so a card can remember a document id. What it cannot do is *be* a scene card: the label is canvas text, so a synopsis, status or word count would be mirrored in and out of a drawing by custom code, and "this card is in Act 2" is geometry, not a fact. Frames can stand for acts, but the order inside one is by position on the screen. The honest split, if used at all, is a blob for freeform drawing next to real structured cards, never cards as shapes.

**Accessibility.** MEASURED on 20 labelled rectangles: the accessibility tree has 91 nodes, none named "Scene N" (the canvas is one unnamed `image`); 21 focusable nodes, all toolbar and chrome; 40 consecutive Tab presses cycle through toolbar buttons and the canvas wrapper and never reach an element. READ: upstream #5759 "[Discussion] Excalidraw accessibility" (open since 2022-10; its text says screen-reader support "seems limited mainly to the interface, not the canvas contents"), #7492 (a Deque audit of a host page, open since 2023-12), #11641 and #11297 (unnamed buttons, open 2026). This is a structural limit of a canvas editor, not a bug list.

**Touch and 390 px.** The toolbar fits (373 px wide). MEASURED at 390 px with touch emulation: 28 of 28 visible controls are smaller than 44 x 44 px. The project's rule is 44 px targets on touch.

**Theming.** The default look is hand-drawn (roughness 1, Excalifont). Set `roughness: 0`, rounded corners, a solid fill and font family 2 and it looks flat (`out/shot-excalidraw-flat-light.png`). But dark mode is an inversion: a cream fill `#fdf6e3` rendered as dark brown (`out/shot-excalidraw-flat-dark.png`), so colours that carry meaning (POV, status) do not stay as designed in both themes. The app cannot supply its own palette per theme.

**Performance.** MEASURED, 4x CPU throttle: pan with 1,500 elements p95 10.2 ms (the display runs at 120 Hz, so p50 is 8.3 ms everywhere). Selecting all 1,500 and dragging them: p95 158 ms, max 167 ms (first run; a later run of mine lost the selection and its figure is not used). At 300 elements everything was smooth. A writer's board is 40 to 60 cards per WRITERS-BOARDS (Sokoloff, August, Snyder), so scale is not the issue.

**Live updates.** Scríobh's events carry ids and revisions only, so a change elsewhere means refetch and swap, as `boards.js` does now. Excalidraw has element-level reconciliation (`reconcileElements`) built for CRDT-like collaboration; not needed here, and a whole-scene blob with a revision check is simpler.

**WebKit and Tauri.** MEASURED in Playwright's WebKit build: the patched bundle draws, types and opens the menu with zero violations and zero errors. 60 open upstream issues mention Safari or WebKit (READ: GitHub search, not triaged). Not tested in the Tauri window itself.

### 3.2 tldraw 5.6.2

READ, `LICENSE.md` of the repository main branch (the installed package's `LICENSE.md` points to it). The tldraw licence permits use in "Development Environments" and forbids "Production Environments" ("any production deployment ... where the software is used to provide functionality to end users"). It forbids disabling or interfering with licence-key enforcement and says "The Software may collect and transmit usage data to tldraw for license compliance purposes." Trial licences exist; commercial licences are by quote; hobby licences keep the "made with tldraw" watermark (READ: tldraw.dev community licence page and a search summary). A self-hosted writing app that anyone may install cannot ship this without a key per installation, and invariant 7 forbids the outbound call. MEASURED: the unlicensed default fetched 36 files from `cdn.tldraw.com` on load, showed the watermark, and under the app's CSP produced 176 console errors. 2.28 MB raw, 679,361 B gzip, 19 gradient declarations in its CSS. The older v1 line was MIT, but I did not evaluate it and it is not maintained by the upstream project as far as I can tell (not verified).

### 3.3 xyflow: React Flow 13.0.2, @xyflow/system 1.0.0, Svelte Flow 2.0.2

MIT. Healthy: pushed 2026-11-19, 39,109 stars, 141 open issues, 15 stable releases in 12 months, 150 contributors. It is DOM-based, which makes it the only candidate here with real focusable nodes. MEASURED: 20 nodes reachable by Tab, each `role=group tabindex=0 aria-roledescription=node`, no `aria-label`; arrow keys move a selected node (the docs say Shift moves faster; after Enter, two Right and one Shift+Down the node sat at x 10, y 20); an assertive live region announces "Moved selected node down. New position, x: 10, y: 20" (READ in the docs that this message is configurable with `ariaLabelConfig`). Zero violations under the strict CSP, no network, in Chromium and WebKit. Weaknesses for this job: free x,y with no lanes, so "move to Act 2" is geometry; React (155,190 B gzip with React, 2,736 B CSS); node positions are written as `style="transform: translate(...)"` through React, which the CSP allows but the smoke test's `[style]` rule counts (29, 309 and 1,509 elements for 20, 300 and 1,500 nodes); the docs ask that the attribution badge stay unless you subscribe to Pro ($169 or $289 a month; READ; the page does not say whether MIT alone requires the badge). A vanilla route exists: `@xyflow/system` (drag and pan-zoom, 26,853 B gzip) leaves rendering to you, and Svelte Flow needs the Svelte compiler (neither built or measured here). Good for a graph of links between scenes and characters; not a grid board.

### 3.4 Konva, Fabric.js, Paper.js

All MIT, all canvas, none depend on a framework. MEASURED sizes: 59,621, 93,550 and 126,979 B gzip. Static greps of their bundles (READ): Paper has one `new Function` (blocked by the CSP); Fabric writes `cssText` through CSSOM (allowed). I did not run them in the browser because the structural problem is the same for all three: a card is pixels on a canvas, so a screen reader sees nothing and focus has nowhere to go without a parallel DOM, and you would still have to write columns, rows, status, colour and the ops. They are drawing engines; a board of cards does not need one.

### 3.5 JointJS, Drawflow

JointJS (`@joint/core` 4.3.4, MPL-2.0, 142,983 B gzip) is an SVG diagramming kit; MPL-2.0 is file-level copyleft, which is workable but one more thing to track. Drawflow (0.0.60, MIT, 9,149 B gzip, 655 B CSS) is small and vanilla but its last release is 2024-09-03 and 274 issues are open (GitHub). Both model nodes and links, not lanes of cards.

### 3.6 Plain DOM and SVG, with a drag helper or by hand

This is the recommendation. Options for the drag part:

- By hand with pointer events: what `outline.js` and `boards.js` already do (classes for drop marks, `is-dragging`, no moved ghost). My spike: 3,252 B raw, 1,387 B gzip including keyboard moves, a live region and the SVG layer.
- SortableJS (12,794 B gzip, full build, last release 2026-02-11), interact.js (29,701 B), Muuri (23,609 B, idle since 2021), dragula (4,339 B, idle since 2024-06), @formkit/drag-and-drop 0.7.0 (8,898 B), Pragmatic drag and drop 4.0.0 (6,956 B in my build; Apache-2.0; native browser drag and drop; assistive controls are guidelines, not code). All would work. None gives keyboard moves or the lanes semantics, so the hand-written code stays; a library only replaces the pointer half, which is the smaller half.

### 3.7 Others looked at

- Penpot (MPL-2.0, 62,621 stars): a full design application, deployed with Docker Compose or Kubernetes. The pages I read describe no embeddable component. Not an option here.
- draw.io (Apache-2.0): a diagram editor designed to be embedded in an iframe; out of scope for a card board and heavy.
- JSON Canvas 1.0 (obsidianmd/jsoncanvas, MIT, READ): an open file format, not a library. `nodes[]` of type `text`, `file`, `link` or `group`, each with `id, type, x, y, width, height` and an optional `color` (hex or the presets "1" to "6"); `edges[]` with `fromNode, toNode, fromSide, toSide, fromEnd, toEnd, label`. The spec leaves preset shades to the application. It maps cleanly onto "free layout": a scene card is a `file` node pointing at the document, a free note is a `text` node, a group is an act. Scríobh's Obsidian importer already skips `.canvas` files and lists them (OPEN-QUESTIONS, "Obsidian vault"), so reading them is a natural later feature.
- Quickdraw and other small whiteboard SDKs surfaced in a search (SNIPPET) and were not examined.

---
## 4. Spike measurements

Setup: each library built with esbuild 0.28.2 (`minify`, `target es2020`, ESM with splitting, `conditions: production`, `NODE_ENV` defined), served by a small Node server with the app's policy from `internal/server/middleware.go`, `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'` (`docs/ARCHITECTURE.md` lists the same policy without `manifest-src` and `worker-src`). My first runs sent the header on the HTML only; the Excalidraw export, probe, accessibility, touch and screenshot runs were repeated with it on every response, as the real server does, and gave the same results where both were run. A route handler aborted and logged any request leaving the origin; a `securitypolicyviolation` listener logged each breach by directive; console errors and warnings and page errors were logged; `[style]` and `<style>` were counted in the live DOM, the same rule `scripts/csp-smoke.mjs` applies (it fails on a `style=` attribute or `<style>` element in the live DOM, which is stricter than the CSP itself).

### 4.1 Bundle sizes (gzip -9; raw in brackets)

| Bundle | Initial JS | CSS | Notes |
|---|---|---|---|
| react + react-dom only | 68,884 (221,952) | | baseline |
| Excalidraw 0.18.1 | 444,873 (1,373,041), 7 files | 22,761 (144,577) | entry alone 249,628; all 180 chunks 2,632,228 (7,891,556); 4 woff2 from CSS 81,144 raw |
| tldraw 5.6.2 | 679,361 (2,277,017) | 14,607 (79,228) | 3 files |
| @xyflow/react 13.0.2 | 155,190 (472,552) | 2,736 (15,122) | includes React |
| @xyflow/system (XYDrag, XYPanZoom) | 26,853 (77,256) | | |
| Konva 10.10.0 | 59,621 (195,277) | | |
| Fabric 7.4.0 | 93,550 (298,847) | | |
| Paper 0.17.0 | 126,979 (352,034) | | |
| @joint/core 4.3.4 | 142,983 (473,358) | | |
| Drawflow 0.0.60 | 9,149 (46,532) | 655 (1,911) | |
| SortableJS 1.15.7 | 12,794 (36,973) | | |
| interact.js 1.10.28 | 29,701 (98,576) | | |
| Muuri 0.9.5 | 23,609 (84,610) | | |
| SVG.js 3.2.8 | 29,757 (91,011) | | |
| Pragmatic drag and drop (element adapter) | 6,956 (22,071) | | |
| @formkit/drag-and-drop 0.7.0 | 8,898 (30,648) | | |
| dragula 3.7.3 | 4,339 (10,691) | | |
| perfect-freehand 1.2.3 | 1,973 (4,499) | | |
| Hand-rolled board and SVG layer | 1,387 (3,252) | 1,021 (2,451) | no gradients |

Against the budgets in `docs/ARCHITECTURE.md`: the editor bundle is 140,078 B and its limit 180 KB. Excalidraw's initial JS is 2.5 times that limit; with CSS 22,761 B it would use the whole per-page 25 KB CSS limit before the 9,222 B base sheet. tldraw is 3.8 times the limit. The hand-rolled spike is less than 1% of the limit.

### 4.2 Strict-CSP and network results

| Run | Violations | Console errors | Off-origin requests | `[style]` after load / after use | `<style>` |
|---|---|---|---|---|---|
| Excalidraw, no CSP, default | none (no policy) | 3 | 1 to esm.sh (font) | 16 / 31 | 0 |
| Excalidraw, CSP, default | 230 `font-src` (esm.sh) | 232 | 0 (blocked) | 16 / 31 | 0 |
| Excalidraw, CSP, asset path set | 230 `font-src` (esm.sh) | 230 | 0 (blocked) | 16 / 31 | 0 |
| Excalidraw, CSP, asset path set, esm.sh patched out | 0 | 0 | 0 | 16 / 30 | 0 |
| same, plus SVG export clicked (policy on every response) | 2: `script-src` eval, `style-src-attr` | 3 (worker timeout, skipped glyph subsetting, inline style), a file-picker abort, and an uncaught EvalError | 0 | n/a | 0 |
| same, in WebKit | 0 | 0 | 0 | 16 / 30 | 0 |
| tldraw, no CSP | none | 68 | 36 to cdn.tldraw.com | 90 / 104 | 1 |
| tldraw, CSP | 22 `img-src`, 16 `font-src`, 1 `style-src-elem`, 1 `img-src blob:` | 176 | 0 (blocked) | 90 / 104 | 1 |
| xyflow (20 nodes), CSP, Chromium and WebKit | 0 | 0 | 0 | 29 / 29 | 0 |
| Hand-rolled board, CSP, Chromium 1280 and 390 px, WebKit | 0 | 0 (WebKit prints one stylesheet message on an empty page too: harness noise) | 0 | 0 / 0, and 0 during a drag | 0 |

Primitive probe under the app's CSP (`csp-probe.mjs`): `eval` and `new Function` blocked; `WebAssembly.compile` blocked (`wasm-eval`); `new Worker(blob:)` blocked (`worker-src`); `setAttribute("style", ...)` blocked (`style-src-attr`). Allowed: `el.style.color = ...` (CSSOM), constructable stylesheets, and SVG `setAttribute("transform", ...)` (applied, matrix e = 30). That last row is why an SVG layer positioned by attributes passes both the CSP and the smoke test, and why the existing pages use SVG for geometry (the cover page's redrawn sketch, the sparklines, the words chart).

House rule check (READ): `scripts/csp-smoke.mjs` has no allow-list for `[style]` or `<style>`; every page it walks must have none in the served HTML and the live DOM. A search of first-party `web/static/js` (everything except `htmx.min.js` and the editor bundle) finds no `.style.` write, no `cssText` and no `setAttribute("style"`; `web/editor/src/schema.js` and `paste.js` say in comments that they avoid reading `element.style` for the same reason. So the house has no exception today, and any library that writes style attributes would be the first.

Static scan of bundles for `new Function`, `eval(`, `setAttribute("style"`, `createElement("style")` and hardcoded https URLs (READ): Paper has one `new Function`; xyflow and tldraw contain `createElement("style")`; Excalidraw has 7 `setAttribute("style"` and 4 `createElement("style")` sites (none fired in my runs); tldraw contains 28 distinct non-spec URLs; the others are clean.

### 4.3 Accessibility, keyboard and touch

| Test | Excalidraw (20 rectangles) | xyflow (20 nodes) | Hand-rolled (40 cards, 40 SVG notes) |
|---|---|---|---|
| AX tree: nodes named "Scene N" other than text | 0 | 0 (nodes unnamed; names only as inner text) | 40 SVG notes named; 40 grip buttons named; cards are focusable `li` with their text |
| Focusable nodes | 21, all chrome | 22 | 121 |
| Tab reaches the cards | No: 40 presses, 0 elements | Yes | Yes |
| Keyboard move | no per-element focus stop (Tab test); arrow-key nudging of a selection was not tested | arrows move a selected node (after Enter, two Right and one Shift+Down it sat at x 10, y 20); live region names no title | Alt+arrows move a card across cells and reorder within one, focus kept, live region says where; arrows move an SVG note 10 px, Shift 50 |
| 390 px, controls under 44 x 44 | 28 of 28 | not measured | 44 x 44 grip; no sideways page scroll (scrollWidth 390) |
| Touch drag | not tested | not tested | works through the grip with CDP touch events (emulated, not a real device) |

Caveats on the board column: cards are `li` with `tabindex=0` and no `aria-labelledby`, a shortcut in the spike; the real one should follow the existing research-board and corkboard markup. Screenshots viewed: the board at 1280 px in light and dark and at 390 px in light (`out/look-board-1280-light.png`, `look-board-1280-dark.png`, `look-board-390-light.png`; flat fills, one accent, readable in both themes). `out/look-board-390-svg.png` also shows what a freeform SVG layer does on a phone: 1,800 units across 390 px makes text about 3 px high, so the free layout needs pan and zoom on a phone, with the grid or a list as the phone's primary view.

### 4.4 Performance

Headless Chromium, scripted drag of 40 pointer moves at 16 ms steps, `requestAnimationFrame` intervals recorded. For the board and xyflow rows the run asserted that the item really moved (a `board:move` event and a new parent cell; a changed `transform` attribute; a changed node transform); my first runs did not, and the numbers from them were discarded. The Excalidraw rows were taken earlier and did not assert the move; the selection UI (608 DOM nodes against 214) shows the select-all took effect, and its pan is not asserted either. The screen ran at 120 Hz (p50 8.3 ms means one frame per refresh). Indicative only; not like for like in what is dragged.

| Items | CPU | Excalidraw | xyflow | Board (DOM) | Board (SVG) |
|---|---|---|---|---|---|
| 300 | 1x | mount 121 ms; drag all p95 10.0 | mount 72; drag one 9.4; pan 9.4 | mount 33; drag one 9.4 | mount 33; drag one 9.4 |
| 300 | 4x | mount 342; drag all p95 10.2 | mount 176; drag one 10.2; pan 10.2 | mount 51; drag one 10.2 | mount 51; drag one 10.2 |
| 1,500 | 4x | mount 399; pan 10.2; **drag all 1,500 p95 158 (max 167)** | mount 269; drag one 10.3; pan 16.6 (max 18.6) | mount 59; drag one 10.2 (max 18.3) | mount 75; drag one 10.2 (max 15.5) |

"Mount" is the time to the first card or canvas in the DOM from navigation start (the board is server-rendered markup, so it needs no script to appear). The writing-board scale in the evidence report (40 to 60 cards, hundreds for a whole book) is far below where anything here slows down; a 1,500-card DOM grid still moved smoothly. Performance does not decide this.

### 4.5 What I built and where

a throwaway spike folder outside the repository (not committed)

- `build.mjs`: esbuild runner and size table (`out/measure.json`); `PATCH=1` applies the esm.sh patch.
- `run.mjs`: server with the app's CSP, request router, violation and console logging, `[style]` counts, optional CPU throttle and WebKit (`BROWSER=webkit`).
- `run-excalidraw*.mjs`, `run-generic.mjs`, `run-board.mjs`, `run-perf.mjs`, `run-ax.mjs`, `run-390.mjs`, `run-touch.mjs`, `run-look.mjs`, `csp-probe.mjs`, `wk-libs.mjs`: the experiments; JSON results in `out/*-results.json`, `out/perf-*.json`, `out/ax-results.json`.
- `src/board.js`, `src/board.css`, `src/board-html.mjs`: the hand-rolled board and SVG layer.
- Screenshots in `out/shot-*.png` (looks were judged from screenshots, not CSS).
- `pw-browsers/` holds Playwright's WebKit (297 MB), outside the repository.

---
## 5. How the recommendation fits Scríobh

### 5.1 The invariants

- **1 to 2, OIDC and SQLite.** Unaffected. Board tables are ordinary tables, one writer.
- **3, one op registry.** Every board action is an op, rendered to HTML, REST, CLI and MCP. A sketch drawn in a canvas library is a blob that only its own editor can change, which defeats the CLI and MCP renderings. The grid does not have that problem. Suggested ops (names for discussion): `board.create`, `board.get`, `board.list`, `board.update` (title, grouping rule), `board.delete`/`restore`, `board.card.place` (board, reference, column key, row key, position), `board.card.add`/`remove`, `board.note.create`/`update` (a free card with its own title, synopsis, colour token), `board.apply` (an explicit, previewed batch of `doc.move` calls that makes the manuscript match the board), and for the free layout `board.card.pin` (x, y) and `board.edge.create`/`delete`. Moves of real scenes inside the grid stay `doc.move`/`doc.update` with the revision the card was drawn from, as `boards.js` does today. `make skill` follows.
- **4, Markdown and the database canonical.** A board is layout, not text. The mirror gets a generated projection (for example `boards/<slug>.canvas` in JSON Canvas, never read back; import stays an explicit action). It is not a second writer. Compile never sees boards.
- **5, Git push-only.** A projection file is pushed like any other; nothing is pulled.
- **6, desktop-ready.** No new origin, no wrapper feature. Playwright's WebKit passed the spike; the Tauri window itself was not tested.
- **7, privacy.** No CDN, no font fetch, no outbound call in the recommended path (measured: zero off-origin requests). The Excalidraw route needs a patch to reach the same state; tldraw cannot.
- **8, the writing surface is sacred.** A board is a separate page. It must follow the rule in `boards.js` that a live update never redraws while something is being edited or dragged.
- **9, no gradients, both themes.** The spike has none and uses CSS custom properties in both themes. Excalidraw's CSS has two gradient declarations and its dark mode inverts element colours.
- **10, small.** A lazy `board.js` registered in the `lazyJS` table in `internal/server/web_test.go` with its own limit (my estimate: at most 9 KB gzip, since `boards.js` is 6.2 KB and `outline.js` 7.2 KB) and a `board.css` of about 2 to 3 KB, inside the 25 KB per-page CSS limit. No new bundle, no React, no change to the editor's exception. A second pinned bundle for Excalidraw would need the invariant amended first (the plan is the contract).
- **11, alternatives are annotations.** A board arrangement must not rewrite the manuscript. Keep board order separate and make "apply to the manuscript" explicit (`board.apply`), the same stance as the alternatives design.
- **12, editorial before generative.** The board needs no AI. Any "suggest an arrangement" feature is optional and later.
- **13, performance is a gate.** Add a board case to `scriobh bench`: a project with 500 scene cards, one page render and one move op, with a published budget.

### 5.2 Fit with the existing screens

- Beat sheet templates (F-094) and beat cards that link to scenes with "unwritten beat" warnings (F-095) are in the feature list with no built-status row that I could find. They are the existing plan for this board's column presets (three act, four part, Save the Cat) and for its free note cards: a beat is a card with no scene yet, and "promote to scene" is the op that links it.
- The corkboard (F-091) and the status board (F-052/F-092) are already lanes of cards with keyboard moves and "Move to" boxes, and no drag (FEATURES: "**No drag**: Earlier and Later, Alt+arrows and Move to another part"). The new board is their natural successor and can share `ccard` markup, `data-rev` revision checks, conflict choice and the live-marker mechanism. FEATURES line 797 rules out "Kanban for everything"; the board should be an optional lens on the manuscript, as WRITERS-BOARDS item 9 says, not a new way in.
- Grouping by data the app owns: columns by part or by status or by story-date bucket, rows by POV entity (`pov_entity_id`), colour by tag token or status, with a letter code beside colour so it reads with colour off (WRITERS-BOARDS item 3). Several boards per project (item 6) means a `boards` table, not one global arrangement.
- Free note cards (the "idea" a writer pins before a scene exists) belong to the board's own table; a "promote to scene" op turns one into a document.

### 5.3 Live updates

Events carry ids, types, revisions and counts. The board follows the house pattern in `boards.js`: on an event, fetch the server's fragment (`/ui/projects/{id}/board`) and swap it, but not while a card is being edited or dragged here; say so in the live region when it lands. No CRDT is needed. A whole-scene Excalidraw blob would be last-write-wins with a revision check and a conflict choice, like a synopsis edit.

### 5.4 Dependencies and build

The recommended path adds none. Keep JS authored in the repository, as `boards.js` is. If a vendored library is ever wanted (perfect-freehand for a pen), follow `web/static/js/VENDOR.md`: pinned, hashed, unmodified, with licence noted.

### 5.5 If Excalidraw is chosen anyway

Use it for a sketch type only (a drawing attached to a project or a board), never for the card board. Conditions, each of which has evidence above:

1. Amend invariant 10 and `docs/ARCHITECTURE.md` first: a second pinned bundle, built in CI like the editor, with the output committed and a Go test that rebuilds it and compares bytes. Own budget row, own `lazyJS`-style test. Expect about 445 KB gzip of JS per session and 22.8 KB gzip of CSS (a per-page CSS limit that is already 25 KB), against 180 KB for the editor. Load it on the sketch page only; the board never loads it.
2. Build with an esbuild plugin that removes the esm.sh fallback (and pin the version, since the string and structure are internal) and serve the fonts from a self-hosted folder through `EXCALIDRAW_ASSET_PATH`. Prune: drop the Chinese fallback fonts (12 of the 13 MB), mermaid and cytoscape, and all locales but English, then measure the real binary delta against the 6 MB of headroom.
3. Decide the CSP. Draw, text, move and select ran clean under the current policy. SVG export with fonts failed under it (measured) and image insert is expected to; they would need `unsafe-eval`, `wasm-unsafe-eval`, `blob:` workers and a `style-src-attr` exemption, none of which the project allows. Either hide those features through the host's props or accept a documented, per-page exception. Export would then have to be done by Scríobh itself.
4. Add a documented exception to the smoke test's `[style]` rule for the vendored subtree, or the page cannot be added to the walk.
5. Pass a flat theme in both modes through scene defaults (roughness 0, fill, font family 2) and accept that dark mode inverts colours.
6. State in the UI and in `docs/ACCESSIBILITY.md` that the sketch is not keyboard or screen-reader operable, and give every sketch a text description field and a text alternative, as the cover page already does for pictures. Provide at least a "text description" op so agents and the CLI have something to read.
7. Store it as `board.sketch.set(board, scene_json, revision)` with a size cap, a revision check and a conflict choice; mirror it as an `.excalidraw` file and a rendered SVG made by Scríobh; treat it as opaque to search, MCP and CLI beyond the description.
8. Keep scene cards out of it. Link a sketch element to a document with `link` if the writer wants a jump, and no more.

---
## 6. Risks and open questions

1. **Is a board a second order of the manuscript?** The recommendation keeps board placement separate from `documents.position` and makes "apply to manuscript" explicit. The alternative, where moving a card on the board is the manuscript move, is simpler, and the existing corkboard already works that way. The owner should choose; the data model differs.
2. **Which dimension is a column, and who decides?** The evidence supports acts, POV, subplot, episode and status, and Sokoloff finds colour for subplots distracting. A board needs a grouping rule stored on the board (part, status, POV entity, tag, story-date bucket, or custom lanes), with custom lanes the one new concept.
3. **Touch drag on a real device.** Touch moves passed through emulated events only. `OPEN-QUESTIONS` already records that touch drag in the outline is untested on real phones. The buttons and menus stay the tested route; the grip is the 44 px addition.
4. **Free layout on a phone.** At 390 px a free canvas needs pan and zoom and is a poor primary view. Plan the grid or list as the phone default, and treat free layout as desktop-first with a keyboard route (arrows nudge, a "Move to" form for exact placement).
5. **`[style]` rule versus library DOM.** If anyone later brings in a React-based library, the smoke test's `[style]` rule will fail on every node. That would be a policy decision (an exception for one vendored subtree), not a quiet change.
6. **Excalidraw fonts and licences.** Its font families carry their own licences, which I did not audit. The pruned font set matters for the binary budget.
7. **Interchange.** JSON Canvas is open and MIT, but "colour" is a hex or six presets, while Scríobh uses eight named tokens. A mapping table is needed for export, and the import of `.canvas` files should say what it dropped.
8. **WebKit.** I tested Playwright's WebKit build on macOS, not the Tauri window and not Safari on iOS. The Linux WebKitGTK build the Arch package uses (see `docs/DESKTOP.md`) was not tested.
9. **Screen reader.** No screen reader was used on anything here, the same gap `docs/ACCESSIBILITY.md` records. Accessibility claims above are from the browser's accessibility tree and keyboard walks.
10. **Evidence limit.** WRITERS-BOARDS found no prevalence number for board use at all. This report recommends a board because documented practice, not measured demand, points at grids of cards.
11. **Performance data are indicative.** Headless, one machine, 120 Hz display, and the drag in each case moved a different thing. The one janky result (Excalidraw, 1,500 elements moved together at 4x CPU throttle) is a bulk-move case, not what a board does.
12. **Maintenance of the hand-rolled path.** It is code to own. The cost is bounded: `boards.js` and `outline.js` already carry the same drag, keyboard and conflict logic and are tested by `e2e-boards.mjs` and `e2e-outline.mjs`.

---
## 7. Sources

Repository (read-only): `AGENTS.md`; `docs/research/WRITERS-BOARDS.md`; `docs/ARCHITECTURE.md` (CSP line 640, budgets 655 to 691); `docs/FEATURES.md` (F-052, F-090 to F-095, F-111, rows 797, 887, 943 to 948); `docs/DATA-MODEL.md` (`documents`, tags, entities); `docs/ACCESSIBILITY.md`; `docs/DESKTOP.md`; `docs/OPEN-QUESTIONS.md` (Obsidian import, touch drag); `scripts/csp-smoke.mjs`; `web/templates/cards.html`, `board.html`, `partials/boards.html`; `web/static/js/boards.js`, `outline.js`, `VENDOR.md`; `internal/server/web_test.go` (`lazyJS`, budget tests); `web/editor/build.mjs`.

Libraries and licences:

- Excalidraw: https://github.com/excalidraw/excalidraw ; npm https://www.npmjs.com/package/@excalidraw/excalidraw ; installation and fonts https://plus.excalidraw.com/docs/sdk/excalidraw/installation (redirect from https://docs.excalidraw.com/docs/@excalidraw/excalidraw/installation) ; issues https://github.com/excalidraw/excalidraw/issues/5759 , /7492 , /7657 , /7672 , /11641 , /11297
- tldraw: https://github.com/tldraw/tldraw/blob/main/LICENSE.md ; https://tldraw.dev/community/license ; https://tldraw.dev/pricing ; https://tldraw.dev/sdk-features/license-key (search result, SNIPPET)
- React Flow and xyflow: https://github.com/xyflow/xyflow ; https://reactflow.dev/learn/advanced-use/accessibility ; https://reactflow.dev/learn/troubleshooting/remove-attribution
- Konva https://github.com/konvajs/konva ; Fabric https://github.com/fabricjs/fabric.js ; Paper https://github.com/paperjs/paper.js ; JointJS https://github.com/clientIO/joint ; Drawflow https://github.com/jerosoler/Drawflow ; SortableJS https://github.com/SortableJS/Sortable ; interact.js https://github.com/taye/interact.js ; Muuri https://github.com/haltu/muuri ; SVG.js https://github.com/svgdotjs/svg.js
- Pragmatic drag and drop https://github.com/atlassian/pragmatic-drag-and-drop ; https://atlassian.design/components/pragmatic-drag-and-drop/about ; @formkit/drag-and-drop https://github.com/formkit/drag-and-drop ; dragula https://github.com/bevacqua/dragula ; perfect-freehand https://github.com/steveruizok/perfect-freehand
- JSON Canvas https://jsoncanvas.org/spec/1.0/ ; https://github.com/obsidianmd/jsoncanvas
- Penpot https://github.com/penpot/penpot ; https://help.penpot.app/technical-guide/getting-started/ ; draw.io https://github.com/jgraph/drawio ; AFFiNE https://github.com/toeverything/AFFiNE
- Repository health: GitHub API (stars, open issues, last push) and npm registry `time` data, read 2026-11-22.
- Search that surfaced alternatives (SNIPPET, not relied on): a general web search for self-hosted embeddable whiteboard components; its results were directory and blog pages.
