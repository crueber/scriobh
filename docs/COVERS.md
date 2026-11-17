# Cover layouts

Status: **planned, being built.** This is the design for F-1200 to F-1208 (section BG of [FEATURES.md](FEATURES.md)). The plan is the contract: change this file before the code. The owner took every recommendation under "Decisions" on 17 November 2026.

## What it is

Today every cover Scríobh makes is generated: one flat colour, the title, subtitle, author and kind of book (F-030, F-230, F-593). A **cover layout** is the writer's own cover. When a project has one, it takes the place of the generated cover everywhere a cover appears. When it has none, nothing changes.

A layout is one of two shapes:

1. **Front and back.** Each side is one of:
   - **generated**: today's typographic cover for that side;
   - **image**: one picture, full bleed, filling the side;
   - **page**: a picture placed on a page of the book's trim, with a flat background colour, a fit (fit inside, fill and crop, actual size), a scale, a position on a nine-point grid, and a margin.

   The back is optional. A project with only a front image is the common case (it is all an ebook ever shows).
2. **One image for the whole wrap.** A single picture that spans back, spine and front, as a printer's wraparound file does. The front and back are cut from it.

The generated cover stays as the fallback and the default. A layout never changes the manuscript's text.

## What other tools do, and what we take from it

From the research (sources in "Sources"):

- **Ebook and print covers are different files.** Vellum, Atticus, Reedsy and KDP all keep the ebook cover (front only) apart from the print wrap. KDP tells authors to leave the back, spine and barcode out of the ebook cover. *We take:* one layout serves both, but the ebook only ever receives the front.
- **Only two tools make a wrap.** Pressbooks generates front, spine and back from page count, paper and text; Draft2Digital generates a wrap from the ebook front, or takes one uploaded file for the whole wrap. Writing apps (Vellum, Atticus, Scrivener, Ulysses) make the ebook cover only and leave the wrap to a designer. *We take:* both of D2D's routes: build the wrap from sides (shape 1), or accept a designer's whole-wrap file (shape 2).
- **A back cover inside a book is just a full-page image.** Atticus and Vellum let a back cover in only as a full-page image in the matter. No standard puts a back cover in an EPUB. *We take:* the EPUB's back cover is an option, off by default, placed as the last page.
- **Low resolution is a warning, never a block, and never upscale.** Vellum, KDP Cover Creator and Apple all say so. *We take:* warnings with the effective dpi at the placed size, as the PDF already does for interior images (`image_low_resolution`).
- **Geometry belongs to the printer, not to us.** KDP and IngramSpark disagree on text margins (0.125 in vs 0.25 in), barcode boxes (2 × 1.2 in vs 1.75 × 1 in), and the spine-text floor (79 vs 48 pages). *We take:* named print profiles (KDP paperback, IngramSpark paperback) with the numbers, and the printer's own template stays authoritative.
- **The spine comes from the page count, and a page count changes.** Pressbooks warns that its wrap is tied to the last PDF's page count. *We take:* a wrap records the spine it was made for, and a change of page count marks it stale; nothing is ever stretched to fit.
- **Manuscripts carry no cover art.** Shunn's format and agents' advice point that way; no source requires it, so this is an inference. *We take:* the manuscript DOCX and the submission bundle never include a cover.

## Where a layout lives

A layout belongs to the **project** (the manuscript), not to any one export: the same cover is wanted in the EPUB, the print wrap, the library and the site. It is stored in its own table, not as a document in the manuscript tree, because a cover is not text: it has no words, no revisions, no goals, no word counts, and the `documents.kind` check would need a rebuild of the largest table to admit it.

It is still **in the manuscript where the writer looks**: the manuscript tree gets a fixed "Cover" row above the first document, which opens the cover page. The row shows a small thumbnail when a layout exists and the generated swatch when it does not. It cannot be moved, trashed or excluded from compile: removing the layout is "Use the generated cover" on the cover page.

(The alternative, cover pages as documents of a new kind, is listed under "Open decisions".)

## Data

Migration `0810_cover_layouts.sql` (the `0800` range is "files, attachments, covers, quotas").

`cover_layouts`, one row per project at most:

| Column | Type | Notes |
|---|---|---|
| `project_id` | TEXT PK | references `projects(id)` on delete cascade |
| `shape` | TEXT | `sides` or `wrap` |
| `trim` | TEXT | the page size the cover is made for, one of the print presets' sizes (`6x9`, `5.5x8.5`, `a5`, …); default `6x9`. It fixes the aspect of every side, on screen and in print |
| `front_json` | TEXT | a side (below); `sides` only |
| `back_json` | TEXT | a side, or `{"mode":"none"}`; `sides` only |
| `wrap_json` | TEXT | the wrap (below); `wrap` only |
| `alt` | TEXT | the cover's description for screen readers; required when any side is an image |
| `revision` | INTEGER | the concurrency token, as documents have |
| `hash` | TEXT | sha256 of the layout and the images' hashes: part of every export's input hash and every cache key |
| `created_by`, `created_at`, `updated_at` | | from Go (`db.Now`) |

A **side**: `{"mode":"generated"}`, `{"mode":"image","attachment":"att_…"}`, or `{"mode":"page","attachment":"att_…","fit":"contain|cover|actual","scale":100,"position":"center","background":"#RRGGBB","margin_in":0.5}`. `position` is one of the nine grid names; `background` is one of the cover palette (no free colour, as today); `scale` is 10 to 100 percent of the fit; `margin_in` is 0 to 1.5.

A **wrap**: `{"attachment":"att_…","bleed_in":0.125}`, at the layout's `trim`. The spine is not stored: it is measured from the picture (below).

**The aspect.** Every side, and the front cut from a wrap, has the trim's proportions (6×9 is 2:3). On screens and in the ebook the front is cut **at the trim**, without bleed; the bleed exists only on the print page. An `image` side whose picture has another aspect is placed by `fill and crop` from its centre (an ebook-shaped 1.6:1 picture loses a sliver top and bottom on a 1.5:1 panel), and the cover page shows the crop before it is saved; a writer who wants the whole picture uses a `page` side with `fit inside`. A print cover made for another trim than the layout's is refused with a sentence naming both (`cover_trim_mismatch`): the layout is made again for the new trim, never stretched.

**Images are attachments.** A cover image is an ordinary project attachment, so it is counted in the quotas, archived by backup, served by `GET /files/{id}` with the usual headers, and deduplicated. The `attachment_refs.ref_kind` check gains `cover` (the migration rebuilds that small table), and the layout writes a `cover` ref for each image it uses. The Markdown mirror copies every attachment that has any reference (`mirror/extras.go`), so it carries the cover with no further change. The Git push is narrower: a public remote carries only attachments whose id appears in a pushed document's text (`gitmirror/audience.go`), so the push **needs a change** to carry `cover` references; whether a public remote should carry the cover is decided per remote by its audience (on by default for a public remote: the cover is the book's face, and its bytes are cleaned). Deleting an attachment that has references is already refused unless forced (`ops.ConflictWith`, "still link to this attachment"); a forced delete of a cover image returns the layout to the generated cover for that side.

**Images are cleaned when they become a cover**, not only at publish: `imagesafe.Clean` runs on upload through the cover ops, and `set` accepts only an attachment of the same project that the cover upload made (a `cover_clean` flag on the attachment row, set by `project.cover_layout.upload`). An ordinary attachment, which is stored as uploaded, cannot be named in a layout without going through the cover upload, which cleans it into a new attachment, so the stored bytes have no EXIF, GPS, XMP or thumbnails, and every surface that shows the cover (site, public Git remote, share links, exports, the library) carries cleaned bytes. A CMYK JPEG, SVG, or anything over the pixel cap is refused at upload with a sentence saying why (the PDF writer is RGB only).

**Derived images** are cached under `files/projects/<id>/covers/<layout hash>-<name>.<ext>` (the path ARCHITECTURE already reserves), made on first use and swept when the layout changes:

- `front`: the front for screens and ebooks, at most 2560 px on the long side (KDP's and Vellum's figure), JPEG at quality 90, sRGB;
- `back`: the same for the back, when there is one;
- `thumb`: 480 px wide, for the library and the tree row.

They are never the source of truth: the attachment and the layout are. They are not in backups (rebuilt on demand).

`projects.cover_path`, which nothing has ever used, is left as it is and documented as unused (dropping it would rebuild `projects`).

## The wrap: measuring and cutting

A wrap picture's size in inches is known from its height: the height is the trim height plus two bleeds, so `dpi = pixel height / (trim height + 2 × bleed)`. Then:

- `wrap width in = pixel width / dpi`;
- `spine in = wrap width − 2 × trim width − 2 × bleed`;
- the front is the outer `trim width + bleed` on the front's side, the back the outer `trim width + bleed` on the other side.

So the front can be cut with no paper thickness and no page count. For a left-to-right book the front is on the right; for a right-to-left book (the project's language direction) it is on the left. The cut is a crop of the decoded picture, encoded once as the `front` derivative.

At upload the measured spine is shown. A spine under zero, or a dpi under 150, is refused (the picture is not a wrap for that trim). When a print cover is made (`export.cover`), the measured spine is compared with the spine computed from the page count and the paper (`compile.cover_spec`). A difference over 1/32 in is the warning `cover_wrap_stale`, with both figures: the picture is placed as it is and never stretched, and the writer gets a new wrap from their designer or the printer's template.

The width alone cannot tell a wrong trim from a different spine: with the height fixed, any error in the chosen trim's width moves into the measured spine. So the trim is not checked at upload beyond the refusals above. A wrong trim shows as a spine that is negative, or wider than any book could need (over 2.5 in), which is refused at upload, or as `cover_wrap_stale` when a print cover is made, where the computed spine disagrees with the measured one. The cover page shows the measured spine in inches and pages (at the chosen paper) so the writer can see at once that a 6×9 template was loaded as 5.5×8.5.

## Where the cover appears

Every consumer of the generated cover today, and what each does with a layout:

| Consumer | No layout (today) | Sides: front image or page | Sides: back | Wrap |
|---|---|---|---|---|
| Library tile and shelf (`project-tile.html`, `shelf.html`) | CSS cover | the `thumb` derivative in an `<img>` with the alt text; the tile keeps its 3:4 box and fits the picture inside it on the cover colour | not shown | `thumb` cut from the front |
| Manuscript tree "Cover" row | swatch | `thumb` | not shown | `thumb` |
| `project.cover` op (`GET /api/v1/projects/{id}/cover`) | SVG, ETag from the spec | the `front` JPEG, ETag from the layout hash; `?format=svg` still returns the generated SVG | n/a | the `front` cut |
| EPUB (`export.compile`, `epub3`) | `images/cover.svg`, `cover-image`, `text/cover.xhtml`, EPUB 2 meta | `images/cover.jpg` as the `cover-image` item; the cover page keeps its own XHTML with one `<img>` and its alt text, no background colour (Kobo's and Apple's guidance); EPUB 2 meta kept. KDP asks for the cover image without an extra HTML cover page; the existing `cover` option (a cover page, default on in the `epub3` preset) stays, and its hint says to turn it off for a file bound for KDP | only with the new option `back_cover` (default off): `text/back-cover.xhtml`, last in the spine, non-linear | front cut; back cut only with `back_cover` |
| HTML export (`export.html`) | inline SVG on the title page | single file: the `front` JPEG as a data URI; zip: `images/cover.jpg` | not shown | front cut |
| Print cover (`export.cover`) | generated wrap: colour, text, spine, barcode box | front panel: the picture full bleed (image) or laid out on its page (page); spine: flat colour and the spine text as today | back panel likewise; `generated` keeps today's back | the picture placed over the whole wrap at its measured size; no text drawn; guides and the barcode box only on the proof page (below) |
| Interior PDF (`export.compile`, print presets) | no cover | unchanged by default; option `cover_page` puts the front as the first page, full bleed (Vellum's "print only" image) | unchanged | unchanged |
| Static site (`show_cover`) | `assets/cover.svg`, 600×800 | `assets/cover.jpg` with its real width and height | not shown | front cut |
| Manuscript DOCX, submission bundle | none | none, by design | none | none |
| Markdown mirror and Git push | none | mirror: `cover/` folder with the images, and the layout (without ids) in `project.json`; Git push: the same, which **needs a change** in the public audience (above) | same | same |
| Backup and restore | the database | the database and the attachments, as for any attachment; derivatives rebuilt | same | same |
| EPUB import (F-818) | the book's cover kept as an attachment | the preview offers "Use as this project's cover"; applying it makes a `sides` layout with that front image | n/a | n/a |
| Share links, reading view | no cover | no cover (unchanged) | | |

Every export's input hash includes the layout hash, so a changed cover is a changed export.

### The print cover with a layout

`export.cover` keeps its inputs (preset, page count, paper thickness, bleed, guides, barcode) and gains `profile` (`kdp`, `ingram`, `generic`, default `generic`), which sets the text-safe margin, the barcode box size and the spine-text floor:

| Profile | Text margin inside trim | Barcode box (lower right of back) | Spine text from | Spine text from the edges |
|---|---|---|---|---|
| `kdp` | 0.125 in | 2 × 1.2 in | 79 pages | 0.0625 in |
| `ingram` | 0.25 in | 1.75 × 1 in | 48 pages | 0.0625 in (0.03125 in under a 0.35 in spine) |
| `generic` | 0.25 in | 2 × 1.2 in | 48 pages | 0.0625 in |

With `sides`, the panels are drawn from the layout and the spine as today (flat colour, rotated title and author, now gated by the profile's page floor as well as the width). With `wrap`, the picture is the whole first page, nothing is drawn over it, and the barcode box is drawn only when asked (the designer may have left room, or the printer adds it). A second page, as today, is the front alone; a third, new, page is a **proof**: the wrap at a reduced size with the trim, bleed, spine folds, text margin and barcode box drawn over it, so the writer can see what a printer will cut. The proof page is never sent to a printer; the guides option keeps its meaning for page one.

The PDF stays RGB (the writer has no CMYK path; see "Not in scope"). The cover check reports the effective dpi of each placed picture (`image_low_resolution` under 300), the stale wrap, and a page-mode picture that crosses the text margin.

**Pixel cap.** A 300-dpi wrap is about 11 megapixels at 6×9 in but 18.5 at US Letter and 19 at A4, over the PDF writer's 16-million cap per picture. The cover's picture gets its own cap of 40 million pixels. A JPEG is passed through without decoding, so memory is not the issue; a PNG wrap over 16 million pixels is refused at upload with the advice to send a JPEG.

## Ops

All registered in the cover package, rendered to REST, CLI and MCP as every op is. `make skill` regenerates the skill and the CLI snapshot.

| Op | Method and path | Auth | Notes |
|---|---|---|---|
| `project.cover_layout.get` | `GET /api/v1/projects/{id}/cover-layout` | reader | the layout, its measured wrap figures, its warnings; `null` when generated |
| `project.cover_layout.upload` | `POST /api/v1/projects/{id}/cover-layout/images?part=front\|back\|wrap` | editor | raw body, `NoDryRun`; sniffs, cleans with `imagesafe`, checks the caps, stores an attachment, returns its id, size, dpi at the chosen trim, warnings |
| `project.cover_layout.set` | `PUT /api/v1/projects/{id}/cover-layout` | editor | the whole layout with `revision`; dry run shows the warnings; writes the `cover` refs |
| `project.cover_layout.clear` | `DELETE /api/v1/projects/{id}/cover-layout` | editor | back to the generated cover; the images stay as attachments until deleted |
| `project.cover_layout.check` | `POST /api/v1/projects/{id}/cover-layout/check` | reader | against a print preset and profile: dpi per side, spine measured vs computed, text margin, barcode box; writes nothing |

`project.cover` changes only as the table above says. The authorization sweep covers the new ops (404 for a project you cannot see). Events: `cover_layout.updated` and `cover_layout.cleared`, named as the existing events are (`attachment.updated`), so webhooks and the audit trail (`1000_events_audit`) see them.

## The cover page

`/p/{slug}/cover`, reached from the tree's Cover row, the project menu, and the Compile page's cover section.

- **Shape:** three choices: the generated cover; front and back; one image for the whole wrap.
- **Front and back:** for each side, generated, image or page. Image and page take an upload (a file input; drag and drop on the drop zone with script). Page shows fit, scale, the nine-point position, the background swatches and the margin.
- **Wrap:** an upload, the trim (the print presets' sizes), the bleed; then the measured dpi and spine, and a drawing of the picture with the cut lines, the spine folds and the text margin.
- **Preview:** the front at the library tile's size and at a page's size, both themes. With script, page mode updates as the controls change (CSS `object-fit` and `object-position` match the server's fit). Without script, Save shows the server's rendering.
- **Alt text:** required when a picture is used, with a hint ("what the cover shows, in a sentence").
- **Warnings** in words, each with what to do: low resolution at the chosen trim, a wrap that does not fit the trim, a stale spine, a picture that is not RGB.

Works at 390 px, with the keyboard alone, after a reload with the network cut (to be shown in the test: whether the page and its last preview are served from the shell's cache, as other project pages are, is to be checked, not assumed), in both themes, and inside the desktop window. Every word goes through the i18n catalogue (`cover.`). The script is lazy, `cover.js` at most 5 KB gzip, inside the budgets in ARCHITECTURE. The CSS goes into the per-page sheet under the 25 KB per-page budget.

## Image processing

Cutting the front from a wrap and making the `front` and `thumb` derivatives needs cropping and scaling. The standard library crops (`SubImage`) and encodes JPEG and PNG, but has no scaler. The plan adds `golang.org/x/image/draw` (the Go team's pure-Go module; CatmullRom for downscaling), pinned in `go.mod`. No CGO, no ImageMagick, no npm. The alternative, a hand-written box filter, is smaller and looks worse; see "Open decisions".

Decoding a 19-megapixel JPEG takes about 76 MB as RGBA, and one at the 40-megapixel cap about 160 MB. Derivative work runs in the existing compile gate (`config.CompileConcurrency`), one at a time per project, and its time is a bench metric (`cover.derive` p95 under 1.5 s for a 6×9 wrap on the reference machine).

## Not in scope

- **A text-over-image designer.** No title or author is drawn over an uploaded picture (KDP Cover Creator does; it is a different product). A page-mode side has a picture and a background, nothing else. The generated cover remains the way to get typeset text.
- **CMYK and PDF/X-1a.** The PDF writer is RGB only. KDP's help says to use CMYK; Ingram asks for it. Many writers' designers deliver the print wrap themselves; the RGB proof and the stale-spine check still help them. Recorded as a known limit, with the printer's upload preview as the check.
- **Hardcover case laminate and dust jackets** (different wrap and hinge geometry per printer).
- **AI cover concepts** (F-211) and any image generation.
- **3D mockups and per-store marketing images.**
- **Covers in the manuscript DOCX or the submission bundle.**

## Phases

**C1, the front.** Migration, storage and cleaning, the five ops, the cover page (shapes, sides, wrap measurement), derivatives, and the screen consumers: library tile, tree row, `project.cover`, EPUB front, HTML export, site, mirror and Git, backup. The EPUB import's "use as cover".
*Exit:* the ops in the authz sweep; e2e of the cover page at 390 px with the keyboard; an EPUB with an image cover passes the EPUB checker and opens in a reading system; a wrap's front cut matches a hand-made cut to the pixel; a cleaned upload has no EXIF (test with a GPS-tagged JPEG); the mirror and a Git push carry the cover; bench metric recorded.

**C2, print.** `export.cover` with layouts and profiles, the proof page, the checks, the Compile page's cover section using the layout, the interior PDF's `cover_page` option, the EPUB `back_cover` option.
*Exit:* covers for each print preset and profile, sides and wrap, checked by `internal/publish/check`; the stale-spine warning proven by changing the page count; a 300-dpi Letter wrap within the new cap.

**Not verified at the end of C2, and said so:** no print-on-demand upload (KDP or Ingram previewer) of a produced cover; no printed proof; no reading system beyond the EPUB checker and one desktop reader; the Compile and cover pages in a real screen reader.

## Changes to the rest of the plan

Made with this design: the FEATURES catalogue rows F-1200 to F-1208, F-031's status row and the BG section, the two deliberate non-features; the DATA-MODEL note on `cover_path`; the ARCHITECTURE `covers/` line; the ROADMAP phase; the OPEN-QUESTIONS entry; the README row. Still to make when the work starts:


- **FEATURES:** status rows for F-230 and F-593 noting that a layout takes their place when present.
- **DATA-MODEL:** the `0810_cover_layouts` table, the `trim` column, the `cover` ref kind and the `cover_clean` flag on attachments.
- **ARCHITECTURE:** the cover pixel cap, the `x/image` dependency, the bench metric, the cover page's script budget.
- **Git push:** the public audience rule for `cover` references (above).

## Decisions

Taken by the owner on 17 November 2026, each as recommended: (1) project data with a fixed Cover row in the tree; (2) a page side is a picture placed on a page, with no text side; (3) one layout per project; (4) `golang.org/x/image/draw` for scaling; (5) KDP and IngramSpark paperback profiles, plus generic. The reasoning for each is kept below.


1. **Cover rows or cover documents.** Recommended: the layout is project data with a fixed Cover row in the tree (above). The alternative follows Vellum and Atticus: cover pages are documents in the tree. That need not rebuild `documents`: a `front_matter` or `back_matter` document with a new `cover_role` column (`front`, `back`, `wrap`) and its picture as content would do. It makes a cover something the writer moves, compiles and trashes like any page, and a picture then carries a document's machinery (revisions, word counts, the editor), and the screen surfaces (library, site, `project.cover`) must find the cover by searching the tree.
2. **"Standard page" means a picture placed on a page.** That is what this design builds. The other reading, an ordinary text page (a back-cover blurb typeset as a page, as Pressbooks' "About" text and KDP Cover Creator's back description do), is not in this design. It could be a third side mode, `text`, using the typesetter and a document from the manuscript as its source.
3. **One layout, or one per format.** This design has one layout per project (the ebook takes its front). Some authors use a different ebook cover from the print front. A per-format override (`ebook_front`) is a small addition if wanted.
4. **The scaler.** `golang.org/x/image/draw` (recommended) or a hand-written filter.
5. **Print profiles.** KDP and IngramSpark paperback to start; Lulu, BookBaby and others later only from their published figures.

## Sources

From the research for this design (17 November 2026; vendor pages where readable):

- Vellum, ebook cover: https://help.vellum.pub/ebook-cover/ ; print: https://help.vellum.pub/print/
- Atticus, back cover for print: https://intercom.help/atticus-5877e36564df/en/articles/13322040-how-do-i-add-the-back-cover-for-print ; cover not exported in print: https://intercom.help/atticus-5877e36564df/en/articles/12758266-why-isn-t-my-book-cover-included-when-i-export ; image sizes: https://intercom.help/atticus-5877e36564df/en/articles/12623469-calculate-image-size-for-print-and-ebooks
- Scrivener, compile cover: https://www.literatureandlatte.com/blog/how-to-create-an-ebook-by-compiling-your-scrivener-project
- Reedsy Studio: https://reedsy.com/studio/resources/how-to-format-a-book/
- Draft2Digital FAQ (ebook cover, generated or uploaded wrap): https://draft2digital.com/faq/
- Pressbooks cover generator (CC BY mirror of its guide): https://openbooks.macewan.ca/authorsguide/chapter/creating-and-uploading-a-book-cover/
- Leanpub, cover size and unbranded EPUB: https://help.leanpub.com/en/articles/110542-what-size-should-my-book-cover-image-be ; https://help.leanpub.com/en/articles/10355937
- Kobo EPUB guide: https://github.com/kobolabs/epub-spec
- KDP ebook cover: https://kdp.amazon.com/en_US/help/topic/G200645690 and https://kdp.amazon.com/en_US/help/topic/G6GTK3T3NUHKLEFX ; paperback cover: https://kdp.amazon.com/en_US/help/topic/G201953020 ; Cover Creator: https://kdp.amazon.com/en_US/help/topic/G201113520 ; hardcover: https://kdp.amazon.com/en_US/help/topic/GDTKFJPNQCBTMRV6 ; manuscript without cover: https://kdp.amazon.com/help?topicId=G200645680
- IngramSpark file requirements: https://www.ingramspark.com/blog/file-requirements-for-print-books
- Apple Books asset guide: https://help.apple.com/itc/booksassetguide/en.lproj/static.html
- EPUB 3.3, `cover-image`: https://www.w3.org/TR/epub-33/ ; structural semantics: https://www.w3.org/TR/epub-ssv-11/ ; DAISY on covers: https://kb.daisy.org/publishing/docs/epub/cover.html
- Shunn, novel manuscript format: https://www.shunn.net/format/novel.html

Not verified in that research: Kobo Writing Life's own specs, IngramSpark's PDF/X-1a wording, the hardcover spine formulas, Send to Kindle's handling of covers, and the cover features (if any) of Novlr, Dabble and Plottr.
