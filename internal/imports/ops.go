package imports

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"runtime"
	"strings"
	"sync"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
)

// The import ops. Preview writes nothing and keeps nothing. Apply re-sends the
// same files with the preview's token (a hash of every accepted file's path and
// content) and the writer's edits, rebuilds the plan, and creates it in one
// transaction. Bodies carry the files as JSON, up to MaxBody; the CLI reads a
// local folder and the UI posts a zip (zip, base64) or a folder listing (files).

const maxListedSkips = 500

// PreviewIn asks what importing a folder would create.
type PreviewIn struct {
	Name              string    `json:"name,omitempty" desc:"What the folder is called; becomes the project title when the files say nothing better."`
	Files             []File    `json:"files,omitempty" desc:"The folder's files. Up to 10,000 entries; at most 2,000 Markdown files, 2 MiB each and 32 MiB together."`
	Zip               []byte    `json:"zip,omitempty" desc:"Instead of files: a zip of the folder, base64."`
	Docx              []byte    `json:"docx,omitempty" desc:"Instead of files or a zip: a Word document (.docx), base64. Headings become chapters, text before the first heading becomes front matter, scene breaks, lists, tables and footnotes are kept; comments, images and tracked changes are reported, not imported."`
	Epub              []byte    `json:"epub,omitempty" desc:"Instead of files, a zip or a Word document: an EPUB 2 or 3 book (.epub), base64. The reading order, metadata, cover, pictures and footnotes are read and the preview proposes parts, chapters and front and back matter, which the edits can rename, merge (merge_into), move (parent), skip and reorder. A book with DRM, or with a fixed layout, is refused. The file is kept intact, with its SHA-256, as an edition of the project (edition.list)."`
	EpubSections      string    `json:"epub_sections,omitempty" enum:"auto,toc,files,headings" desc:"With epub: how the text is cut into sections: toc (the book's table of contents), files (one for each file of its reading order), headings (at the chapter-level heading, for one long file), auto (default: toc, else files, else headings). The preview's epub.strategy says which was used."`
	EpubPages         string    `json:"epub_pages,omitempty" enum:"skip,keep" desc:"With epub: skip (default) leaves out the cover page and the title page, which the project's own title and author replace; keep imports a title page as front matter. The contents page is always left out."`
	Pandoc            []byte    `json:"pandoc,omitempty" desc:"Instead of files, a zip or a .docx: a document in a format this program does not read itself, base64, converted to Markdown by the Pandoc program when an administrator has switched the bridge on (pandoc.config.get). Give its format as pandoc_format. Headings become chapters as for a .docx (docx_headings applies). Pictures are taken out of the file only with pictures: import (otherwise each is marked [image]); those Pandoc extracts are checked and cleaned like any import's; raw HTML and Scríobh syntax in the converted text are neutralised; the file is never kept, its hash is recorded."`
	PandocFormat      string    `json:"pandoc_format,omitempty" enum:"odt,rtf,latex,mediawiki,org,textile,rst,fb2,typst,docbook" desc:"With pandoc: odt, rtf, latex, mediawiki, org, textile, rst, fb2, typst or docbook."`
	Pdf               []byte    `json:"pdf,omitempty" desc:"Instead of files, a zip or a .docx: a PDF with a text layer, base64 (up to 32 MiB). The text is read, with its chapters taken from the outline or from headings; running heads, page numbers and a table of contents are removed, footnotes are kept, and the preview says what could not be recovered (scanned pages, tables, pictures, columns, right-to-left text) with a confidence for each page. A scanned or encrypted PDF is refused; nothing in a PDF is run."`
	Text              []byte    `json:"text,omitempty" desc:"Instead of files, a zip or a .docx: a plain text file (.txt, UTF-8, up to 16 MiB), base64. Blank lines separate paragraphs; a line of * * * or --- is a scene break; the characters Markdown would take for markup are escaped. Chapters are found from what the words say: a heading is never assumed, a chapter label (Chapter 3) in capitals is a chapter, and a plain one is offered in the preview (breaks.suggestions) to take; docx_split, docx_split_pattern and docx_breaks cut it as they cut a Word document."`
	Layout            string    `json:"layout,omitempty" enum:"auto,obsidian,scrivener,wordpress,ghost" desc:"obsidian reads the files as an Obsidian vault (wiki links, embeds, callouts, comments, dataview); it is never detected: say so. scrivener reads a Scrivener project (send files or a zip of the .scriv folder), wordpress a WordPress export (WXR, an .xml file) and ghost a Ghost export (.json); auto (default) recognises these three by their own markers when there is no Markdown to read, and otherwise reads a Scríobh mirror or export, or a generic folder. The preview's layout says what was read."`
	DocxHeadings      string    `json:"docx_headings,omitempty" enum:"auto,chapters,parts" desc:"With docx: leave empty (or auto) to have the heading level and the parts chosen for you (see docx_split). chapters makes the lowest heading level the chapters, with no parts; parts makes it a part and the next level the chapters."`
	DocxSplit         string    `json:"docx_split,omitempty" enum:"auto,heading1,heading2,style,label,page_breaks,section_breaks,numerals,size,pattern,none" desc:"With docx or text (and a pdf or pandoc source that has no headings): how the document is cut into chapters. auto (default) cuts at strong boundaries only, all used together: a heading of the chapter level (the lowest level used twice; a book title at Heading 1 over Heading 2 chapters, and Part headings over chapters, are told apart without a setting), a paragraph in a custom style named for chapters, and a 'Chapter 3' line that looks like a title (centred, bold, capitals, larger or after a page break), with an unformatted one that belongs to a numbered run. A page break, a section break, a lone numeral or a plain label alone never cuts under auto; the preview lists them as breaks.suggestions and each can be chosen: heading1 or heading2 cut at that heading level, style at docx_split_style, label at every line that begins with a chapter word and a number, page_breaks, section_breaks and numerals at those, size at pieces of docx_split_words, pattern at the paragraphs that match docx_split_pattern, none leaves one document. docx_breaks adds chapters of the writer's own under any of them."`
	DocxSplitStyle    string    `json:"docx_split_style,omitempty" desc:"With docx_split style: the paragraph style's name (the preview's breaks.options lists those that exist)."`
	DocxSplitPattern  string    `json:"docx_split_pattern,omitempty" desc:"With docx_split pattern: the text a chapter's first line begins with (docx_split_match starts), or an RE2 regular expression the line matches (regex). At most 200 characters. It is tried only against paragraphs of 200 characters or fewer, so a long paragraph is never a chapter start; a regular expression with a very large program, or a book with too much short text to try it on, is refused. The preview's breaks.pattern says how many paragraphs it matches, and the first five. A pattern kept with another docx_split changes nothing but that answer."`
	DocxSplitMatch    string    `json:"docx_split_match,omitempty" enum:"starts,regex" desc:"With docx_split_pattern: starts (default) matches the beginning of a paragraph, ignoring case; regex reads the pattern as an RE2 expression, case as typed (begin it with (?i) to ignore case)."`
	DocxSplitTry      string    `json:"docx_split_try,omitempty" desc:"With docx: a pattern to try. It changes nothing: the preview's breaks.try says how many paragraphs it matches (docx_split_try_match says how to read it, as docx_split_match does), and the first five."`
	DocxSplitTryMatch string    `json:"docx_split_try_match,omitempty" enum:"starts,regex"`
	DocxSplitWords    int       `json:"docx_split_words,omitempty" desc:"With docx_split size: about how many words a piece has, 500 to 20,000 (default 5,000). The cut is at the nearest scene break within a fifth, else at a paragraph's end."`
	DocxBreaks        []int     `json:"docx_breaks,omitempty" desc:"With docx, text, pdf or pandoc: paragraphs a chapter starts at in addition to what docx_split finds, by ordinal (the preview's breaks.suggestions and each document's ordinal give them): the paragraph's index among the document's paragraphs. At most 5,000. A chapter is taken away by joining it into the one before."`
	DocxChanges       string    `json:"docx_changes,omitempty" enum:"suggest,accept,reject" desc:"With docx: what tracked changes become. suggest (default) imports the text as its author left it, every change rejected, and proposes each change inside a paragraph as a pending suggestion by its Word author (a label, not an account) at the time Word recorded; accepting them all in review gives the reviewers' text. Changes that cannot be suggestions (a whole paragraph inserted or deleted, a heading, a table, a footnote, code, over the caps of 300 per document and 1,000 per import, or ones the review checks would refuse) are taken as the reviewer left them, and the preview counts each kind. accept imports the final view and reject the original, with no suggestions. Word comments are imported as comments by their Word authors in every mode."`
	ScrivResearch     string    `json:"scrivener_research,omitempty" enum:"notes,skip" desc:"With a Scrivener project: notes (default) imports the Research folder as research notes (a picture becomes a note showing it, with pictures: import; PDFs and web pages are named in a note, not imported); skip leaves Research out."`
	ScrivNotes        string    `json:"scrivener_notes,omitempty" enum:"private,skip" desc:"With a Scrivener project: private (default) puts each document's notes after its text as private notes; skip leaves them out."`
	WPStatus          string    `json:"wp_status,omitempty" enum:"published,all" desc:"With a WordPress export: published (default) imports published posts and pages only; all adds drafts, pending, scheduled and private ones, which are marked do not compile."`
	WPTypes           string    `json:"wp_types,omitempty" enum:"posts,pages,both" desc:"With a WordPress export: posts (default), pages, or both (pages go in a part called Pages after the posts)."`
	GhostStatus       string    `json:"ghost_status,omitempty" enum:"published,all" desc:"With a Ghost export: published (default) imports published posts and pages only; all adds drafts, scheduled and sent ones, which are marked do not compile."`
	GhostTypes        string    `json:"ghost_types,omitempty" enum:"posts,pages,both" desc:"With a Ghost export: posts (default), pages, or both."`
	Pictures          string    `json:"pictures,omitempty" enum:"import,skip" desc:"With a Scrivener project, or a WordPress or Ghost export sent as a zip with its images folder: import (default) stores the pictures that are in what was sent as attachments of the project, checked and cleaned of their location, camera and other details, and links the text to them; skip leaves them out (each is marked [image]). A picture is never fetched from an address."`
	ScrivSheets       string    `json:"scrivener_sheets,omitempty" enum:"notes,entities" desc:"With a Scrivener project: notes (default) imports the Characters and Places folders as research notes; entities makes each character and place sheet an entity of the project's primary Compendium (a new project gets one, owned by you; adding to a project uses its primary Compendium if you may edit it). A name the Compendium already has, or a sheet with a picture, stays a research note, and the result says so."`
	SplitScenes       string    `json:"split_scenes,omitempty" enum:"none,h2" desc:"h2 turns the second-level headings of each file into scenes. Default none."`
	Edits             []Edit    `json:"edits,omitempty" desc:"Optional: preview with these edits applied, exactly as apply would."`
	Reorder           []Reorder `json:"reorder,omitempty" desc:"Optional: preview with this reordering applied."`
	StageID           string    `json:"stage_id,omitempty" desc:"Instead of the files: the id import.stage returned. The staged file, the options and the edits are then the draft's (review it with import.draft.update); send nothing else of the source."`
	sum               string    // the staged source's SHA-256, for the cache of its conversion
}

// Validate checks the options.
func (in *PreviewIn) Validate() error {
	return validateCommon(in.DocxChanges, in.SplitScenes, in.Layout, in.DocxHeadings, in.conv())
}

// AppendPreviewIn is PreviewIn for adding to an existing project.
type AppendPreviewIn struct {
	ProjectID         string    `path:"id" json:"-" desc:"Project id."`
	Name              string    `json:"name,omitempty" desc:"What the folder is called."`
	Files             []File    `json:"files,omitempty" desc:"The folder's files, as for import.preview."`
	Zip               []byte    `json:"zip,omitempty" desc:"Instead of files: a zip of the folder, base64."`
	Docx              []byte    `json:"docx,omitempty" desc:"Instead of files or a zip: a Word document (.docx), base64. Headings become chapters, text before the first heading becomes front matter, scene breaks, lists, tables and footnotes are kept; comments, images and tracked changes are reported, not imported."`
	Epub              []byte    `json:"epub,omitempty" desc:"Instead of files, a zip or a Word document: an EPUB 2 or 3 book (.epub), base64. The reading order, metadata, cover, pictures and footnotes are read and the preview proposes parts, chapters and front and back matter, which the edits can rename, merge (merge_into), move (parent), skip and reorder. A book with DRM, or with a fixed layout, is refused. The file is kept intact, with its SHA-256, as an edition of the project (edition.list)."`
	EpubSections      string    `json:"epub_sections,omitempty" enum:"auto,toc,files,headings" desc:"With epub: how the text is cut into sections: toc (the book's table of contents), files (one for each file of its reading order), headings (at the chapter-level heading, for one long file), auto (default: toc, else files, else headings). The preview's epub.strategy says which was used."`
	EpubPages         string    `json:"epub_pages,omitempty" enum:"skip,keep" desc:"With epub: skip (default) leaves out the cover page and the title page, which the project's own title and author replace; keep imports a title page as front matter. The contents page is always left out."`
	Pandoc            []byte    `json:"pandoc,omitempty" desc:"Instead of files, a zip or a .docx: a document in a format this program does not read itself, base64, converted to Markdown by the Pandoc program when an administrator has switched the bridge on (pandoc.config.get). Give its format as pandoc_format. Headings become chapters as for a .docx (docx_headings applies). Pictures are taken out of the file only with pictures: import (otherwise each is marked [image]); those Pandoc extracts are checked and cleaned like any import's; raw HTML and Scríobh syntax in the converted text are neutralised; the file is never kept, its hash is recorded."`
	PandocFormat      string    `json:"pandoc_format,omitempty" enum:"odt,rtf,latex,mediawiki,org,textile,rst,fb2,typst,docbook" desc:"With pandoc: odt, rtf, latex, mediawiki, org, textile, rst, fb2, typst or docbook."`
	Pdf               []byte    `json:"pdf,omitempty" desc:"Instead of files, a zip or a .docx: a PDF with a text layer, base64 (up to 32 MiB). The text is read, with its chapters taken from the outline or from headings; running heads, page numbers and a table of contents are removed, footnotes are kept, and the preview says what could not be recovered (scanned pages, tables, pictures, columns, right-to-left text) with a confidence for each page. A scanned or encrypted PDF is refused; nothing in a PDF is run."`
	Text              []byte    `json:"text,omitempty" desc:"Instead of files, a zip or a .docx: a plain text file (.txt, UTF-8, up to 16 MiB), base64. Blank lines separate paragraphs; a line of * * * or --- is a scene break; the characters Markdown would take for markup are escaped. Chapters are found from what the words say: a heading is never assumed, a chapter label (Chapter 3) in capitals is a chapter, and a plain one is offered in the preview (breaks.suggestions) to take; docx_split, docx_split_pattern and docx_breaks cut it as they cut a Word document."`
	Layout            string    `json:"layout,omitempty" enum:"auto,obsidian,scrivener,wordpress,ghost" desc:"obsidian reads the files as an Obsidian vault (wiki links, embeds, callouts, comments, dataview); it is never detected: say so. scrivener reads a Scrivener project (send files or a zip of the .scriv folder), wordpress a WordPress export (WXR, an .xml file) and ghost a Ghost export (.json); auto (default) recognises these three by their own markers when there is no Markdown to read, and otherwise reads a Scríobh mirror or export, or a generic folder. The preview's layout says what was read."`
	DocxHeadings      string    `json:"docx_headings,omitempty" enum:"auto,chapters,parts" desc:"With docx: leave empty (or auto) to have the heading level and the parts chosen for you (see docx_split). chapters makes the lowest heading level the chapters, with no parts; parts makes it a part and the next level the chapters."`
	DocxSplit         string    `json:"docx_split,omitempty" enum:"auto,heading1,heading2,style,label,page_breaks,section_breaks,numerals,size,pattern,none" desc:"With docx or text (and a pdf or pandoc source that has no headings): how the document is cut into chapters. auto (default) cuts at strong boundaries only, all used together: a heading of the chapter level (the lowest level used twice; a book title at Heading 1 over Heading 2 chapters, and Part headings over chapters, are told apart without a setting), a paragraph in a custom style named for chapters, and a 'Chapter 3' line that looks like a title (centred, bold, capitals, larger or after a page break), with an unformatted one that belongs to a numbered run. A page break, a section break, a lone numeral or a plain label alone never cuts under auto; the preview lists them as breaks.suggestions and each can be chosen: heading1 or heading2 cut at that heading level, style at docx_split_style, label at every line that begins with a chapter word and a number, page_breaks, section_breaks and numerals at those, size at pieces of docx_split_words, pattern at the paragraphs that match docx_split_pattern, none leaves one document. docx_breaks adds chapters of the writer's own under any of them."`
	DocxSplitStyle    string    `json:"docx_split_style,omitempty" desc:"With docx_split style: the paragraph style's name (the preview's breaks.options lists those that exist)."`
	DocxSplitPattern  string    `json:"docx_split_pattern,omitempty" desc:"With docx_split pattern: the text a chapter's first line begins with (docx_split_match starts), or an RE2 regular expression the line matches (regex). At most 200 characters. It is tried only against paragraphs of 200 characters or fewer, so a long paragraph is never a chapter start; a regular expression with a very large program, or a book with too much short text to try it on, is refused. The preview's breaks.pattern says how many paragraphs it matches, and the first five. A pattern kept with another docx_split changes nothing but that answer."`
	DocxSplitMatch    string    `json:"docx_split_match,omitempty" enum:"starts,regex" desc:"With docx_split_pattern: starts (default) matches the beginning of a paragraph, ignoring case; regex reads the pattern as an RE2 expression, case as typed (begin it with (?i) to ignore case)."`
	DocxSplitTry      string    `json:"docx_split_try,omitempty" desc:"With docx: a pattern to try. It changes nothing: the preview's breaks.try says how many paragraphs it matches (docx_split_try_match says how to read it, as docx_split_match does), and the first five."`
	DocxSplitTryMatch string    `json:"docx_split_try_match,omitempty" enum:"starts,regex"`
	DocxSplitWords    int       `json:"docx_split_words,omitempty" desc:"With docx_split size: about how many words a piece has, 500 to 20,000 (default 5,000). The cut is at the nearest scene break within a fifth, else at a paragraph's end."`
	DocxBreaks        []int     `json:"docx_breaks,omitempty" desc:"With docx, text, pdf or pandoc: paragraphs a chapter starts at in addition to what docx_split finds, by ordinal (the preview's breaks.suggestions and each document's ordinal give them): the paragraph's index among the document's paragraphs. At most 5,000. A chapter is taken away by joining it into the one before."`
	DocxChanges       string    `json:"docx_changes,omitempty" enum:"suggest,accept,reject" desc:"With docx: what tracked changes become. suggest (default) imports the text as its author left it, every change rejected, and proposes each change inside a paragraph as a pending suggestion by its Word author (a label, not an account) at the time Word recorded; accepting them all in review gives the reviewers' text. Changes that cannot be suggestions (a whole paragraph inserted or deleted, a heading, a table, a footnote, code, over the caps of 300 per document and 1,000 per import, or ones the review checks would refuse) are taken as the reviewer left them, and the preview counts each kind. accept imports the final view and reject the original, with no suggestions. Word comments are imported as comments by their Word authors in every mode."`
	ScrivResearch     string    `json:"scrivener_research,omitempty" enum:"notes,skip" desc:"With a Scrivener project: notes (default) imports the Research folder as research notes (a picture becomes a note showing it, with pictures: import; PDFs and web pages are named in a note, not imported); skip leaves Research out."`
	ScrivNotes        string    `json:"scrivener_notes,omitempty" enum:"private,skip" desc:"With a Scrivener project: private (default) puts each document's notes after its text as private notes; skip leaves them out."`
	WPStatus          string    `json:"wp_status,omitempty" enum:"published,all" desc:"With a WordPress export: published (default) imports published posts and pages only; all adds drafts, pending, scheduled and private ones, which are marked do not compile."`
	WPTypes           string    `json:"wp_types,omitempty" enum:"posts,pages,both" desc:"With a WordPress export: posts (default), pages, or both (pages go in a part called Pages after the posts)."`
	GhostStatus       string    `json:"ghost_status,omitempty" enum:"published,all" desc:"With a Ghost export: published (default) imports published posts and pages only; all adds drafts, scheduled and sent ones, which are marked do not compile."`
	GhostTypes        string    `json:"ghost_types,omitempty" enum:"posts,pages,both" desc:"With a Ghost export: posts (default), pages, or both."`
	Pictures          string    `json:"pictures,omitempty" enum:"import,skip" desc:"With a Scrivener project, or a WordPress or Ghost export sent as a zip with its images folder: import (default) stores the pictures that are in what was sent as attachments of the project, checked and cleaned of their location, camera and other details, and links the text to them; skip leaves them out (each is marked [image]). A picture is never fetched from an address."`
	ScrivSheets       string    `json:"scrivener_sheets,omitempty" enum:"notes,entities" desc:"With a Scrivener project: notes (default) imports the Characters and Places folders as research notes; entities makes each character and place sheet an entity of the project's primary Compendium (a new project gets one, owned by you; adding to a project uses its primary Compendium if you may edit it). A name the Compendium already has, or a sheet with a picture, stays a research note, and the result says so."`
	SplitScenes       string    `json:"split_scenes,omitempty" enum:"none,h2"`
	Edits             []Edit    `json:"edits,omitempty" desc:"Optional: preview with these edits applied."`
	Reorder           []Reorder `json:"reorder,omitempty" desc:"Optional: preview with this reordering applied."`
	StageID           string    `json:"stage_id,omitempty" desc:"Instead of the files: the id import.stage returned. The staged file, the options and the edits are then the draft's (review it with import.draft.update); send nothing else of the source."`
	sum               string
}

func (in AppendPreviewIn) ProjectRef() string { return in.ProjectID }

// Validate checks the options.
func (in *AppendPreviewIn) Validate() error {
	return validateCommon(in.DocxChanges, in.SplitScenes, in.Layout, in.DocxHeadings, in.conv())
}

// ApplyIn creates a project from a previewed folder.
type ApplyIn struct {
	Token            string    `json:"token" desc:"The token import.preview returned for these same files."`
	Name             string    `json:"name,omitempty" desc:"As in the preview."`
	Files            []File    `json:"files,omitempty" desc:"The same files that were previewed."`
	Zip              []byte    `json:"zip,omitempty" desc:"The same zip that was previewed, base64."`
	Docx             []byte    `json:"docx,omitempty" desc:"The same .docx that was previewed, base64."`
	Epub             []byte    `json:"epub,omitempty" desc:"Instead of files, a zip or a Word document: an EPUB 2 or 3 book (.epub), base64. The reading order, metadata, cover, pictures and footnotes are read and the preview proposes parts, chapters and front and back matter, which the edits can rename, merge (merge_into), move (parent), skip and reorder. A book with DRM, or with a fixed layout, is refused. The file is kept intact, with its SHA-256, as an edition of the project (edition.list)."`
	EpubSections     string    `json:"epub_sections,omitempty" enum:"auto,toc,files,headings" desc:"With epub: how the text is cut into sections: toc (the book's table of contents), files (one for each file of its reading order), headings (at the chapter-level heading, for one long file), auto (default: toc, else files, else headings). The preview's epub.strategy says which was used."`
	EpubPages        string    `json:"epub_pages,omitempty" enum:"skip,keep" desc:"With epub: skip (default) leaves out the cover page and the title page, which the project's own title and author replace; keep imports a title page as front matter. The contents page is always left out."`
	Pandoc           []byte    `json:"pandoc,omitempty" desc:"The same file that was previewed, base64 (with pandoc_format)."`
	PandocFormat     string    `json:"pandoc_format,omitempty" enum:"odt,rtf,latex,mediawiki,org,textile,rst,fb2,typst,docbook" desc:"As in the preview."`
	Pdf              []byte    `json:"pdf,omitempty" desc:"Instead of files, a zip or a .docx: a PDF with a text layer, base64 (up to 32 MiB). The text is read, with its chapters taken from the outline or from headings; running heads, page numbers and a table of contents are removed, footnotes are kept, and the preview says what could not be recovered (scanned pages, tables, pictures, columns, right-to-left text) with a confidence for each page. A scanned or encrypted PDF is refused; nothing in a PDF is run."`
	Text             []byte    `json:"text,omitempty" desc:"Instead of files, a zip or a .docx: a plain text file (.txt, UTF-8, up to 16 MiB), base64. Blank lines separate paragraphs; a line of * * * or --- is a scene break; the characters Markdown would take for markup are escaped. Chapters are found from what the words say: a heading is never assumed, a chapter label (Chapter 3) in capitals is a chapter, and a plain one is offered in the preview (breaks.suggestions) to take; docx_split, docx_split_pattern and docx_breaks cut it as they cut a Word document."`
	Layout           string    `json:"layout,omitempty" enum:"auto,obsidian,scrivener,wordpress,ghost" desc:"As in the preview."`
	DocxHeadings     string    `json:"docx_headings,omitempty" enum:"auto,chapters,parts" desc:"As in the preview."`
	DocxSplit        string    `json:"docx_split,omitempty" enum:"auto,heading1,heading2,style,label,page_breaks,section_breaks,numerals,size,pattern,none" desc:"With docx or text (and a pdf or pandoc source that has no headings): how the document is cut into chapters. auto (default) cuts at strong boundaries only, all used together: a heading of the chapter level (the lowest level used twice; a book title at Heading 1 over Heading 2 chapters, and Part headings over chapters, are told apart without a setting), a paragraph in a custom style named for chapters, and a 'Chapter 3' line that looks like a title (centred, bold, capitals, larger or after a page break), with an unformatted one that belongs to a numbered run. A page break, a section break, a lone numeral or a plain label alone never cuts under auto; the preview lists them as breaks.suggestions and each can be chosen: heading1 or heading2 cut at that heading level, style at docx_split_style, label at every line that begins with a chapter word and a number, page_breaks, section_breaks and numerals at those, size at pieces of docx_split_words, pattern at the paragraphs that match docx_split_pattern, none leaves one document. docx_breaks adds chapters of the writer's own under any of them."`
	DocxSplitStyle   string    `json:"docx_split_style,omitempty" desc:"With docx_split style: the paragraph style's name (the preview's breaks.options lists those that exist)."`
	DocxSplitPattern string    `json:"docx_split_pattern,omitempty" desc:"With docx_split pattern: the text a chapter's first line begins with (docx_split_match starts), or an RE2 regular expression the line matches (regex). At most 200 characters. It is tried only against paragraphs of 200 characters or fewer, so a long paragraph is never a chapter start; a regular expression with a very large program, or a book with too much short text to try it on, is refused. The preview's breaks.pattern says how many paragraphs it matches, and the first five. A pattern kept with another docx_split changes nothing but that answer."`
	DocxSplitMatch   string    `json:"docx_split_match,omitempty" enum:"starts,regex" desc:"With docx_split_pattern: starts (default) matches the beginning of a paragraph, ignoring case; regex reads the pattern as an RE2 expression, case as typed (begin it with (?i) to ignore case)."`
	DocxSplitWords   int       `json:"docx_split_words,omitempty" desc:"With docx_split size: about how many words a piece has, 500 to 20,000 (default 5,000). The cut is at the nearest scene break within a fifth, else at a paragraph's end."`
	DocxBreaks       []int     `json:"docx_breaks,omitempty" desc:"With docx, text, pdf or pandoc: paragraphs a chapter starts at in addition to what docx_split finds, by ordinal (the preview's breaks.suggestions and each document's ordinal give them): the paragraph's index among the document's paragraphs. At most 5,000. A chapter is taken away by joining it into the one before."`
	DocxChanges      string    `json:"docx_changes,omitempty" enum:"suggest,accept,reject" desc:"With docx: what tracked changes become. suggest (default) imports the text as its author left it, every change rejected, and proposes each change inside a paragraph as a pending suggestion by its Word author (a label, not an account) at the time Word recorded; accepting them all in review gives the reviewers' text. Changes that cannot be suggestions (a whole paragraph inserted or deleted, a heading, a table, a footnote, code, over the caps of 300 per document and 1,000 per import, or ones the review checks would refuse) are taken as the reviewer left them, and the preview counts each kind. accept imports the final view and reject the original, with no suggestions. Word comments are imported as comments by their Word authors in every mode."`
	ScrivResearch    string    `json:"scrivener_research,omitempty" enum:"notes,skip" desc:"With a Scrivener project: notes (default) imports the Research folder as research notes (a picture becomes a note showing it, with pictures: import; PDFs and web pages are named in a note, not imported); skip leaves Research out."`
	ScrivNotes       string    `json:"scrivener_notes,omitempty" enum:"private,skip" desc:"With a Scrivener project: private (default) puts each document's notes after its text as private notes; skip leaves them out."`
	WPStatus         string    `json:"wp_status,omitempty" enum:"published,all" desc:"With a WordPress export: published (default) imports published posts and pages only; all adds drafts, pending, scheduled and private ones, which are marked do not compile."`
	WPTypes          string    `json:"wp_types,omitempty" enum:"posts,pages,both" desc:"With a WordPress export: posts (default), pages, or both (pages go in a part called Pages after the posts)."`
	GhostStatus      string    `json:"ghost_status,omitempty" enum:"published,all" desc:"With a Ghost export: published (default) imports published posts and pages only; all adds drafts, scheduled and sent ones, which are marked do not compile."`
	GhostTypes       string    `json:"ghost_types,omitempty" enum:"posts,pages,both" desc:"With a Ghost export: posts (default), pages, or both."`
	Pictures         string    `json:"pictures,omitempty" enum:"import,skip" desc:"With a Scrivener project, or a WordPress or Ghost export sent as a zip with its images folder: import (default) stores the pictures that are in what was sent as attachments of the project, checked and cleaned of their location, camera and other details, and links the text to them; skip leaves them out (each is marked [image]). A picture is never fetched from an address."`
	ScrivSheets      string    `json:"scrivener_sheets,omitempty" enum:"notes,entities" desc:"With a Scrivener project: notes (default) imports the Characters and Places folders as research notes; entities makes each character and place sheet an entity of the project's primary Compendium (a new project gets one, owned by you; adding to a project uses its primary Compendium if you may edit it). A name the Compendium already has, or a sheet with a picture, stays a research note, and the result says so."`
	SplitScenes      string    `json:"split_scenes,omitempty" enum:"none,h2" desc:"As in the preview."`
	Title            string    `json:"title,omitempty" desc:"Project title; default the preview's."`
	Kind             string    `json:"kind,omitempty" enum:"novel,novella,short,collection,nonfiction,essay,blog,report,blank" desc:"Project kind; default the preview's."`
	Author           string    `json:"author,omitempty"`
	Language         string    `json:"language,omitempty"`
	Edits            []Edit    `json:"edits,omitempty" desc:"Renames, kind changes, skips, merges, moves and per-file splits, by node key."`
	Reorder          []Reorder `json:"reorder,omitempty" desc:"New order for one parent's children."`
	EditionLabel     string    `json:"edition_label,omitempty" desc:"With epub: what the edition is called (default: the preview's epub.label, the title and year)."`
	UseCover         bool      `json:"use_cover,omitempty" desc:"With epub: make the book's cover picture the new project's own cover (a layout of one front image, with the book's title as its description). The preview's epub.cover.can_use_as_cover says whether it may be one; a picture that may not is left as an attachment and the result says so."`
	StageID          string    `json:"stage_id,omitempty" desc:"Instead of the files: the draft import.stage made. Its file, options and edits are used; send also rev and the preview's token."`
	Rev              int       `json:"rev,omitempty" desc:"With stage_id: the draft's revision you reviewed (import.draft.get). A draft that has moved on is refused."`
	sum              string
	draft            string // the staged draft this applies: recorded with the source, so a start-up can tell whether an apply committed
}

// Validate checks the options.
func (in *ApplyIn) Validate() error {
	return validateCommon(in.DocxChanges, in.SplitScenes, in.Layout, in.DocxHeadings, in.conv())
}

// AppendIn adds a previewed folder to an existing project.
type AppendIn struct {
	ProjectID        string    `path:"id" json:"-" desc:"Project id."`
	ParentID         string    `json:"parent_id,omitempty" desc:"Folder to add into; empty for the end of the top level."`
	Token            string    `json:"token" desc:"The token import.append_preview returned for these same files."`
	Name             string    `json:"name,omitempty"`
	Files            []File    `json:"files,omitempty"`
	Zip              []byte    `json:"zip,omitempty"`
	Docx             []byte    `json:"docx,omitempty" desc:"The same .docx that was previewed, base64."`
	Epub             []byte    `json:"epub,omitempty" desc:"Instead of files, a zip or a Word document: an EPUB 2 or 3 book (.epub), base64. The reading order, metadata, cover, pictures and footnotes are read and the preview proposes parts, chapters and front and back matter, which the edits can rename, merge (merge_into), move (parent), skip and reorder. A book with DRM, or with a fixed layout, is refused. The file is kept intact, with its SHA-256, as an edition of the project (edition.list)."`
	EpubSections     string    `json:"epub_sections,omitempty" enum:"auto,toc,files,headings" desc:"With epub: how the text is cut into sections: toc (the book's table of contents), files (one for each file of its reading order), headings (at the chapter-level heading, for one long file), auto (default: toc, else files, else headings). The preview's epub.strategy says which was used."`
	EpubPages        string    `json:"epub_pages,omitempty" enum:"skip,keep" desc:"With epub: skip (default) leaves out the cover page and the title page, which the project's own title and author replace; keep imports a title page as front matter. The contents page is always left out."`
	Pandoc           []byte    `json:"pandoc,omitempty" desc:"The same file that was previewed, base64 (with pandoc_format)."`
	PandocFormat     string    `json:"pandoc_format,omitempty" enum:"odt,rtf,latex,mediawiki,org,textile,rst,fb2,typst,docbook" desc:"As in the preview."`
	Pdf              []byte    `json:"pdf,omitempty" desc:"Instead of files, a zip or a .docx: a PDF with a text layer, base64 (up to 32 MiB). The text is read, with its chapters taken from the outline or from headings; running heads, page numbers and a table of contents are removed, footnotes are kept, and the preview says what could not be recovered (scanned pages, tables, pictures, columns, right-to-left text) with a confidence for each page. A scanned or encrypted PDF is refused; nothing in a PDF is run."`
	Text             []byte    `json:"text,omitempty" desc:"Instead of files, a zip or a .docx: a plain text file (.txt, UTF-8, up to 16 MiB), base64. Blank lines separate paragraphs; a line of * * * or --- is a scene break; the characters Markdown would take for markup are escaped. Chapters are found from what the words say: a heading is never assumed, a chapter label (Chapter 3) in capitals is a chapter, and a plain one is offered in the preview (breaks.suggestions) to take; docx_split, docx_split_pattern and docx_breaks cut it as they cut a Word document."`
	Layout           string    `json:"layout,omitempty" enum:"auto,obsidian,scrivener,wordpress,ghost"`
	DocxHeadings     string    `json:"docx_headings,omitempty" enum:"auto,chapters,parts"`
	DocxSplit        string    `json:"docx_split,omitempty" enum:"auto,heading1,heading2,style,label,page_breaks,section_breaks,numerals,size,pattern,none" desc:"With docx or text (and a pdf or pandoc source that has no headings): how the document is cut into chapters. auto (default) cuts at strong boundaries only, all used together: a heading of the chapter level (the lowest level used twice; a book title at Heading 1 over Heading 2 chapters, and Part headings over chapters, are told apart without a setting), a paragraph in a custom style named for chapters, and a 'Chapter 3' line that looks like a title (centred, bold, capitals, larger or after a page break), with an unformatted one that belongs to a numbered run. A page break, a section break, a lone numeral or a plain label alone never cuts under auto; the preview lists them as breaks.suggestions and each can be chosen: heading1 or heading2 cut at that heading level, style at docx_split_style, label at every line that begins with a chapter word and a number, page_breaks, section_breaks and numerals at those, size at pieces of docx_split_words, pattern at the paragraphs that match docx_split_pattern, none leaves one document. docx_breaks adds chapters of the writer's own under any of them."`
	DocxSplitStyle   string    `json:"docx_split_style,omitempty" desc:"With docx_split style: the paragraph style's name (the preview's breaks.options lists those that exist)."`
	DocxSplitPattern string    `json:"docx_split_pattern,omitempty" desc:"With docx_split pattern: the text a chapter's first line begins with (docx_split_match starts), or an RE2 regular expression the line matches (regex). At most 200 characters. It is tried only against paragraphs of 200 characters or fewer, so a long paragraph is never a chapter start; a regular expression with a very large program, or a book with too much short text to try it on, is refused. The preview's breaks.pattern says how many paragraphs it matches, and the first five. A pattern kept with another docx_split changes nothing but that answer."`
	DocxSplitMatch   string    `json:"docx_split_match,omitempty" enum:"starts,regex" desc:"With docx_split_pattern: starts (default) matches the beginning of a paragraph, ignoring case; regex reads the pattern as an RE2 expression, case as typed (begin it with (?i) to ignore case)."`
	DocxSplitWords   int       `json:"docx_split_words,omitempty" desc:"With docx_split size: about how many words a piece has, 500 to 20,000 (default 5,000). The cut is at the nearest scene break within a fifth, else at a paragraph's end."`
	DocxBreaks       []int     `json:"docx_breaks,omitempty" desc:"With docx, text, pdf or pandoc: paragraphs a chapter starts at in addition to what docx_split finds, by ordinal (the preview's breaks.suggestions and each document's ordinal give them): the paragraph's index among the document's paragraphs. At most 5,000. A chapter is taken away by joining it into the one before."`
	DocxChanges      string    `json:"docx_changes,omitempty" enum:"suggest,accept,reject" desc:"With docx: what tracked changes become. suggest (default) imports the text as its author left it, every change rejected, and proposes each change inside a paragraph as a pending suggestion by its Word author (a label, not an account) at the time Word recorded; accepting them all in review gives the reviewers' text. Changes that cannot be suggestions (a whole paragraph inserted or deleted, a heading, a table, a footnote, code, over the caps of 300 per document and 1,000 per import, or ones the review checks would refuse) are taken as the reviewer left them, and the preview counts each kind. accept imports the final view and reject the original, with no suggestions. Word comments are imported as comments by their Word authors in every mode."`
	ScrivResearch    string    `json:"scrivener_research,omitempty" enum:"notes,skip" desc:"With a Scrivener project: notes (default) imports the Research folder as research notes (a picture becomes a note showing it, with pictures: import; PDFs and web pages are named in a note, not imported); skip leaves Research out."`
	ScrivNotes       string    `json:"scrivener_notes,omitempty" enum:"private,skip" desc:"With a Scrivener project: private (default) puts each document's notes after its text as private notes; skip leaves them out."`
	WPStatus         string    `json:"wp_status,omitempty" enum:"published,all" desc:"With a WordPress export: published (default) imports published posts and pages only; all adds drafts, pending, scheduled and private ones, which are marked do not compile."`
	WPTypes          string    `json:"wp_types,omitempty" enum:"posts,pages,both" desc:"With a WordPress export: posts (default), pages, or both (pages go in a part called Pages after the posts)."`
	GhostStatus      string    `json:"ghost_status,omitempty" enum:"published,all" desc:"With a Ghost export: published (default) imports published posts and pages only; all adds drafts, scheduled and sent ones, which are marked do not compile."`
	GhostTypes       string    `json:"ghost_types,omitempty" enum:"posts,pages,both" desc:"With a Ghost export: posts (default), pages, or both."`
	Pictures         string    `json:"pictures,omitempty" enum:"import,skip" desc:"With a Scrivener project, or a WordPress or Ghost export sent as a zip with its images folder: import (default) stores the pictures that are in what was sent as attachments of the project, checked and cleaned of their location, camera and other details, and links the text to them; skip leaves them out (each is marked [image]). A picture is never fetched from an address."`
	ScrivSheets      string    `json:"scrivener_sheets,omitempty" enum:"notes,entities" desc:"With a Scrivener project: notes (default) imports the Characters and Places folders as research notes; entities makes each character and place sheet an entity of the project's primary Compendium (a new project gets one, owned by you; adding to a project uses its primary Compendium if you may edit it). A name the Compendium already has, or a sheet with a picture, stays a research note, and the result says so."`
	SplitScenes      string    `json:"split_scenes,omitempty" enum:"none,h2"`
	Edits            []Edit    `json:"edits,omitempty"`
	Reorder          []Reorder `json:"reorder,omitempty"`
	EditionLabel     string    `json:"edition_label,omitempty" desc:"With epub: what the edition is called (default: the preview's epub.label)."`
	StageID          string    `json:"stage_id,omitempty" desc:"Instead of the files: the draft import.stage made for this project. Its file, options and edits are used; send also rev and the preview's token."`
	Rev              int       `json:"rev,omitempty" desc:"With stage_id: the draft's revision you reviewed."`
	sum              string
	draft            string // the staged draft this applies: recorded with the source, so a start-up can tell whether an apply committed
}

func (in AppendIn) ProjectRef() string { return in.ProjectID }

// Validate checks the options.
func (in *AppendIn) Validate() error {
	return validateCommon(in.DocxChanges, in.SplitScenes, in.Layout, in.DocxHeadings, in.conv())
}

func validateCommon(changes, split, layout, headings string, o convOpts) error {
	if err := checkChanges(changes); err != nil {
		return err
	}
	return checkSource(split, layout, headings, o)
}

func checkChanges(v string) error {
	if v != "" && v != DocxSuggest && v != DocxAccept && v != DocxReject {
		return ops.Invalid("docx_changes", "docx_changes is suggest, accept or reject.")
	}
	return nil
}

func checkSource(split, layout, headings string, o convOpts) error {
	if err := checkSplit(split); err != nil {
		return err
	}
	if o.pandocFmt != "" && !pandoc.ValidImport(o.pandocFmt) {
		return ops.Invalid("pandoc_format", "pandoc_format is one of "+strings.Join(pandoc.ImportFormats(), ", ")+".")
	}
	switch layout {
	case "", "auto", Obsidian, LayoutScrivener, LayoutWordPress, LayoutGhost:
	default:
		return ops.Invalid("layout", "layout is auto, obsidian, scrivener, wordpress or ghost.")
	}
	if headings != "" && headings != "auto" && headings != DocxChapters && headings != DocxParts {
		return ops.Invalid("docx_headings", "docx_headings is auto, chapters or parts.")
	}
	if err := checkCut(o); err != nil {
		return err
	}
	for _, c := range []struct{ field, v, a, b, d string }{
		{"scrivener_research", o.scrivResearch, ScrivResearchNotes, ScrivResearchSkip, ""},
		{"scrivener_notes", o.scrivNotes, ScrivNotesPrivate, ScrivNotesSkip, ""},
		{"wp_status", o.wpStatus, "published", "all", ""},
		{"wp_types", o.wpTypes, "posts", "pages", "both"},
		{"ghost_status", o.ghostStatus, "published", "all", ""},
		{"ghost_types", o.ghostTypes, "posts", "pages", "both"},
		{"pictures", o.pictures, PicturesImport, PicturesSkip, ""},
		{"scrivener_sheets", o.scrivSheets, SheetsNotes, SheetsEntities, ""},
		{"epub_pages", o.epubPages, EpubPagesSkip, EpubPagesKeep, ""},
	} {
		if c.v != "" && c.v != c.a && c.v != c.b && (c.d == "" || c.v != c.d) {
			return ops.Invalid(c.field, c.field+" is "+c.a+", "+c.b+map[bool]string{true: " or " + c.d, false: ""}[c.d != ""]+".")
		}
	}
	switch o.epubSections {
	case "", EpubSectionsAuto, EpubSectionsTOC, EpubSectionsFiles, EpubSectionsHeadings:
	default:
		return ops.Invalid("epub_sections", "epub_sections is auto, toc, files or headings.")
	}
	return nil
}

func (in *PreviewIn) conv() convOpts {
	return convOpts{scrivResearch: in.ScrivResearch, scrivNotes: in.ScrivNotes, wpStatus: in.WPStatus, wpTypes: in.WPTypes, ghostStatus: in.GhostStatus, ghostTypes: in.GhostTypes, pictures: in.Pictures, scrivSheets: in.ScrivSheets, pandocFmt: in.PandocFormat, epubSections: in.EpubSections, epubPages: in.EpubPages, docxSplit: in.DocxSplit, docxStyle: in.DocxSplitStyle, docxWords: in.DocxSplitWords, docxBreaks: in.DocxBreaks, docxPattern: in.DocxSplitPattern, docxMatch: in.DocxSplitMatch, docxTry: in.DocxSplitTry, docxTryMatch: in.DocxSplitTryMatch}
}
func (in *AppendPreviewIn) conv() convOpts {
	return convOpts{scrivResearch: in.ScrivResearch, scrivNotes: in.ScrivNotes, wpStatus: in.WPStatus, wpTypes: in.WPTypes, ghostStatus: in.GhostStatus, ghostTypes: in.GhostTypes, pictures: in.Pictures, scrivSheets: in.ScrivSheets, pandocFmt: in.PandocFormat, epubSections: in.EpubSections, epubPages: in.EpubPages, docxSplit: in.DocxSplit, docxStyle: in.DocxSplitStyle, docxWords: in.DocxSplitWords, docxBreaks: in.DocxBreaks, docxPattern: in.DocxSplitPattern, docxMatch: in.DocxSplitMatch, docxTry: in.DocxSplitTry, docxTryMatch: in.DocxSplitTryMatch}
}
func (in *ApplyIn) conv() convOpts {
	return convOpts{scrivResearch: in.ScrivResearch, scrivNotes: in.ScrivNotes, wpStatus: in.WPStatus, wpTypes: in.WPTypes, ghostStatus: in.GhostStatus, ghostTypes: in.GhostTypes, pictures: in.Pictures, scrivSheets: in.ScrivSheets, pandocFmt: in.PandocFormat, epubSections: in.EpubSections, epubPages: in.EpubPages, docxSplit: in.DocxSplit, docxStyle: in.DocxSplitStyle, docxWords: in.DocxSplitWords, docxBreaks: in.DocxBreaks, docxPattern: in.DocxSplitPattern, docxMatch: in.DocxSplitMatch}
}
func (in *AppendIn) conv() convOpts {
	return convOpts{scrivResearch: in.ScrivResearch, scrivNotes: in.ScrivNotes, wpStatus: in.WPStatus, wpTypes: in.WPTypes, ghostStatus: in.GhostStatus, ghostTypes: in.GhostTypes, pictures: in.Pictures, scrivSheets: in.ScrivSheets, pandocFmt: in.PandocFormat, epubSections: in.EpubSections, epubPages: in.EpubPages, docxSplit: in.DocxSplit, docxStyle: in.DocxSplitStyle, docxWords: in.DocxSplitWords, docxBreaks: in.DocxBreaks, docxPattern: in.DocxSplitPattern, docxMatch: in.DocxSplitMatch}
}

// checkCut validates the options of the Word cut.
func checkCut(o convOpts) error {
	switch o.docxSplit {
	case "", SplitAuto, SplitHeading1, SplitHeading2, SplitStyle, SplitLabel, SplitPageBreaks, SplitSectionBreaks, SplitNumerals, SplitSize, SplitPattern, SplitNone:
	default:
		return ops.Invalid("docx_split", "docx_split is auto, heading1, heading2, style, label, page_breaks, section_breaks, numerals, size, pattern or none.")
	}
	if len([]rune(o.docxStyle)) > maxSplitStyle {
		return ops.Invalid("docx_split_style", "A style name is at most 200 characters.")
	}
	if o.docxSplit == SplitStyle && strings.TrimSpace(o.docxStyle) == "" {
		return ops.Invalid("docx_split_style", "Name the style to cut at.")
	}
	if o.docxWords != 0 && (o.docxWords < MinSplitWords || o.docxWords > MaxSplitWords) {
		return ops.Invalid("docx_split_words", fmt.Sprintf("docx_split_words is %d to %d.", MinSplitWords, MaxSplitWords))
	}
	switch o.docxMatch {
	case "", MatchStarts, MatchRegex:
	default:
		return ops.Invalid("docx_split_match", "docx_split_match is starts or regex.")
	}
	if o.docxSplit == SplitPattern || o.docxPattern != "" {
		if _, perr := compilePattern(o.docxPattern, o.docxMatch); perr != nil {
			return perr.asOps()
		}
	}
	switch o.docxTryMatch {
	case "", MatchStarts, MatchRegex:
	default:
		return ops.Invalid("docx_split_try_match", "docx_split_try_match is starts or regex.")
	}
	if o.docxTry != "" {
		if _, perr := compilePattern(o.docxTry, o.docxTryMatch); perr != nil {
			return perr.asOps()
		}
	}
	if len(o.docxBreaks) > MaxExplicitBrks {
		return ops.Invalid("docx_breaks", fmt.Sprintf("At most %d breaks.", MaxExplicitBrks))
	}
	for _, b := range o.docxBreaks {
		if b < 1 || b > maxParagraphs {
			return ops.Invalid("docx_breaks", "A break is a paragraph's ordinal: 1 or more.")
		}
	}
	return nil
}

// cutOpts are the options the Word cut reads.
func (o convOpts) cutOpts() docxCutOpts {
	return docxCutOpts{split: o.docxSplit, style: strings.TrimSpace(o.docxStyle), words: o.docxWords, add: o.docxBreaks, pattern: o.docxPattern, match: o.docxMatch, try: o.docxTry, tryMatch: o.docxTryMatch}
}

func checkSplit(s string) error {
	if s != "" && s != "none" && s != "h2" {
		return ops.Invalid("split_scenes", "split_scenes is none or h2.")
	}
	return nil
}

// PreviewOut is the proposed import. Nothing has been written.
type PreviewOut struct {
	Token           string           `json:"token" desc:"Send this back with the same files to apply."`
	Layout          string           `json:"layout" enum:"markdown-folder,scriobh,obsidian,docx,pdf,scrivener,wordpress,ghost,pandoc,epub,text"`
	ManifestFile    string           `json:"manifest_file,omitempty" desc:"scriobh.json or project.json when the Scríobh layout was recognised."`
	Project         ProjectInfo      `json:"project" desc:"What a new project would be called and be."`
	Nodes           []Node           `json:"nodes" desc:"The proposed documents in reading order; nest them by parent_key."`
	Documents       int              `json:"documents"`
	Words           int              `json:"words"`
	Skipped         []Skipped        `json:"skipped" desc:"Files that will not be imported, with the reason (the first 500)."`
	SkippedTotal    int              `json:"skipped_total"`
	SkippedByReason map[string]int   `json:"skipped_by_reason"`
	Warnings        []string         `json:"warnings"`
	Research        []ResearchNode   `json:"research" desc:"Research notes the import would write (a Scrivener project's Research folder), in order; nest them by parent_key. Edits by key can rename one or leave it (and what is inside it) out."`
	ResearchTotal   int              `json:"research_total" desc:"How many research notes and folders."`
	Report          map[string]any   `json:"report,omitempty" desc:"What the converter read and did not keep, by count (labels, keywords, comments, images, links ...). The same counts are written to the import's record."`
	Epub            *EpubInfo        `json:"epub,omitempty" desc:"With an EPUB: what the book says about itself, how it was cut, and the map from its files to the proposed documents."`
	Pictures        int              `json:"pictures" desc:"Pictures that came with the source and would be stored as attachments of the project, cleaned (with pictures: import)."`
	PictureBytes    int64            `json:"picture_bytes" desc:"Their size, cleaned, before any the project already holds are counted out."`
	Sheets          int              `json:"sheets" desc:"Research notes marked as character or place sheets that would be made entities (with scrivener_sheets: entities), names permitting."`
	Suggestions     int              `json:"suggestions" desc:"Pending suggestions the import would write from a Word file's tracked changes, for the documents that would be created."`
	Comments        int              `json:"comments" desc:"Comments the import would write from a Word file's comments (by their Word authors, as labels), for the documents that would be created."`
	Breaks          *BreakInfo       `json:"breaks,omitempty" desc:"With a Word document: how it was cut and why, the other ways to cut it with how many chapters each makes, and the places a chapter could start that were not taken by themselves (a page break, a section break, a lone numeral)."`
	LeftOut         []LeftOutNode    `json:"left_out,omitempty" desc:"Documents the import left out on its own (a Word file's contents page). Put one back with the edit keep."`
	Already         *AlreadyImported `json:"already_imported,omitempty" desc:"Only when adding to a project: the project has already had this same file (the same SHA-256; a folder: the same files) and still holds some of its documents. Importing it again adds the text a second time; import.undo takes the earlier import back."`
	Elsewhere       *ElsewhereImport `json:"imported_elsewhere,omitempty" desc:"Only for a new project: an earlier import of this same file (the same SHA-256) into a project the caller can read, which still holds some of its documents. A project the caller cannot read is never named or counted."`
	srcHash         string           // what apply records as the source's hash: the file's own, or a folder's token
}

// ApplyOut says what was created.
type ApplyOut struct {
	ProjectID       string   `json:"project_id"`
	Slug            string   `json:"slug,omitempty" desc:"The new project's slug; empty when importing into an existing project."`
	Title           string   `json:"title,omitempty"`
	Documents       int      `json:"documents"`
	Words           int      `json:"words"`
	SourceID        string   `json:"source_id" desc:"The recorded source (project_sources) of these documents."`
	EditionID       string   `json:"edition_id,omitempty" desc:"With an EPUB: the edition recorded for the book (edition.get), whose source file is kept."`
	FirstDocumentID string   `json:"first_document_id,omitempty"`
	SkippedTotal    int      `json:"skipped_total"`
	ResearchNotes   int      `json:"research_notes,omitempty" desc:"Research notes and folders written."`
	Pictures        int      `json:"pictures,omitempty" desc:"Pictures stored as attachments of the project (or found there already, with the same bytes)."`
	Entities        int      `json:"entities,omitempty" desc:"Character and place sheets made entities (scrivener_sheets: entities)."`
	Suggestions     int      `json:"suggestions,omitempty" desc:"Pending suggestions written from a Word file's tracked changes."`
	Comments        int      `json:"comments,omitempty" desc:"Comments written from a Word file's comments."`
	CompendiumID    string   `json:"compendium_id,omitempty" desc:"The primary Compendium made for the new project to hold those entities."`
	CoverUsed       bool     `json:"cover_used,omitempty" desc:"With use_cover: the book's cover picture is now the project's own cover (project.cover_layout.get)."`
	Warnings        []string `json:"warnings"`
}

// Register adds the import ops.
func Register(r *ops.Registry, limits ...StageLimits) {
	var lim StageLimits
	if len(limits) > 0 {
		lim = limits[0]
	}
	registerDrafts(r, lim)
	registerUndo(r)
	ops.Register(r, ops.Op{Name: "import.preview", Method: "POST", Path: "/api/v1/imports/preview", MaxBody: MaxBody,
		Desc: "Preview importing a folder of Markdown (or a Scríobh mirror or export), a Word document, an Obsidian vault, a Scrivener project, a WordPress export, a Ghost export or an EPUB book (epub: version 2 or 3) as a new project: the proposed tree, word counts, research notes, the files that will be skipped and why, and a report of what was read and not kept, and the pictures that would be stored. For an EPUB the answer also has epub: the book's metadata, how it was cut into sections, and a map from its files to the proposed documents; the edits (rename, kind, skip, merge_into, parent, split) and reorder change the mapping, and apply refuses any that name a document the book does not have. Writes nothing. Limits: 10,000 entries (40,000 for a Scrivener zip), 2,000 Markdown files, 2 MiB per file, 32 MiB together; an EPUB of at most 32 MiB; the request body is at most 48 MiB, so send a large Scrivener project as a zip.",
		Auth: ops.Writer()}, previewNew)
	ops.Register(r, ops.Op{Name: "import.apply", Method: "POST", Path: "/api/v1/imports", Status: 201, MaxBody: MaxBody,
		Desc: "Create a new project from a previewed folder, document, book or export, with the writer's edits, in one transaction: all of it or none (documents, research notes, pictures as attachments, entities when asked, links, search). An EPUB is also kept, intact and hashed, as an edition of the project (edition.list). Send the same files and options and the preview's token.",
		Auth: ops.Writer()}, applyNew)
	ops.Register(r, ops.Op{Name: "import.append_preview", Method: "POST", Path: "/api/v1/projects/{id}/import/preview", MaxBody: MaxBody,
		Desc: "Preview adding a folder of Markdown, a Word document, an Obsidian vault, a Scrivener project, a WordPress export, a Ghost export or an EPUB book to an existing project as new documents at the end (and research notes after the research there). Writes nothing.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, previewInto)
	ops.Register(r, ops.Op{Name: "import.append", Method: "POST", Path: "/api/v1/projects/{id}/import", Status: 201, MaxBody: MaxBody,
		Desc: "Add a previewed folder, document, book or export to an existing project as new documents after what is already there (and research notes after the research there), in one transaction. Existing documents are never changed. An EPUB is kept as an edition of the project.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, appendInto)
}

// source is what every op takes: files or a zip, and the mapping options.
type source struct {
	name, split string
	files       []File
	zip         []byte
	docx        []byte
	pandoc      []byte          // a document in a format Pandoc reads (the bridge, when it is on)
	pandocFmt   string          // its format, from the allow-list
	pandocSvc   *pandoc.Service // the bridge; nil in a build or a test without it
	pdf         []byte
	text        []byte // a plain text file
	epub        []byte
	label       string           // the edition's label, from apply
	staged      *editions.Staged // the book's file, staged for the edition record (an EPUB)
	pics        *applyPictures   // where the pictures went (the cover's address)
	layout      string           // "obsidian", "scrivener", "wordpress", "ghost", or "" for a Scríobh or generic folder (or a converted source recognised by its markers)
	conv        convOpts         // options of the converters
	headings    string           // DOCX: chapters or parts
	changes     string           // DOCX: what tracked changes become (suggest, accept, reject)
	ctx         context.Context  // the caller: a conversion stops when it goes away
	noDetect    bool             // never read the files as a converted source (a repository)
	prov        *Provenance      // where the files came from, when it is not a folder upload
	draft       string           // the staged draft being applied (recorded in the source's metadata)
	sum         string           // a staged source's SHA-256: the cache of its conversion is keyed by it, not by hashing the bytes again
}

// Provenance says where files came from when a caller other than the folder
// upload (the repository import) hands the importer a file set. It is recorded
// in project_sources.
type Provenance struct {
	Kind   string         // "git"
	URL    string         // the address, never with credentials
	Path   string         // what the person called it
	Commit string         // the commit the files were read from
	Meta   map[string]any // extra, merged into metadata_json
}

// PreviewFiles previews a file set the caller already holds, with the writer's
// edits applied exactly as ApplyFiles would apply them. It writes nothing.
func PreviewFiles(files []File, name, split string, edits []Edit, reorders []Reorder) (PreviewOut, error) {
	if err := checkSplit(split); err != nil {
		return PreviewOut{}, err
	}
	return preview(source{name: name, split: split, files: files, noDetect: true}, edits, reorders, alts.DefaultLimits)
}

// ApplyFiles creates a new project from a file set the caller already holds
// (in.Files and in.Zip are ignored), in one transaction, and records prov as
// its source. The token must be the one PreviewFiles returned for these files.
func ApplyFiles(c *ops.Ctx, files []File, in ApplyIn, prov Provenance) (ApplyOut, error) {
	in.Files, in.Zip = files, nil
	return applyNewWith(c, in, &prov)
}

func (s source) list() ([]File, error) {
	if len(s.zip) > 0 {
		if len(s.files) > 0 {
			return nil, ops.Invalid("zip", "Send files or a zip, not both.")
		}
		fs, err := ReadZip(s.zip)
		if err != nil {
			return nil, ops.Invalid("zip", err.Error())
		}
		return fs, nil
	}
	return s.files, nil
}

func (s source) intake() (*intake, error) {
	layout := s.layout
	switch {
	case len(s.docx) > 0:
		layout = LayoutDocx
	case len(s.epub) > 0:
		layout = LayoutEpub
	case len(s.pandoc) > 0:
		layout = LayoutPandoc
	case len(s.pdf) > 0:
		layout = LayoutPdf
	case len(s.text) > 0:
		layout = LayoutText
	case layout == "" && s.prov == nil && !s.noDetect: // a repository is only ever Markdown
		layout = detectConverted(s.files, s.zip)
	}
	converted := (layout == LayoutDocx || layout == LayoutText) && s.sum != "" || layout == LayoutScrivener || layout == LayoutWordPress || layout == LayoutGhost || layout == LayoutPdf || layout == LayoutPandoc || layout == LayoutEpub
	var key string
	if converted {
		if key = s.cacheKey(layout); key != "" {
			if in := cacheGet(key); in != nil {
				return in, nil
			}
		}
	}
	// Reading a source is done a few at a time for the whole process (limiter.go).
	release, err := acquire(s.ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	conversions.Add(1)
	in, err := s.read(layout)
	if err == nil && converted {
		cachePut(key, s.sum, in)
	}
	return in, err
}

// read converts the source into the intake the plan is built from.
func (s source) read(layout string) (*intake, error) {
	kinds := 0
	for _, has := range []bool{len(s.epub) > 0, len(s.pandoc) > 0, len(s.pdf) > 0, len(s.text) > 0, len(s.docx) > 0, len(s.zip) > 0, len(s.files) > 0} {
		if has {
			kinds++
		}
	}
	if len(s.epub) > 0 {
		if kinds > 1 {
			return nil, ops.Invalid("epub", "Send an .epub, or a .docx, or files, or a zip: one of them.")
		}
		return s.convertEpub()
	}
	if len(s.pandoc) > 0 {
		if kinds > 1 {
			return nil, ops.Invalid("pandoc", "Send one document for Pandoc, or files, or a zip, or a .docx: one of them.")
		}
		return s.pandocIntake()
	}
	if len(s.pdf) > 0 {
		if kinds > 1 {
			return nil, ops.Invalid("pdf", "Send a PDF, or a .docx, or files, or a zip: one of them.")
		}
		fs, pre, err := pdfToFiles(s.ctx, s.pdf, s.name, s.conv.cutOpts(), s.sum)
		if err != nil {
			var oe *ops.Error
			if errors.As(err, &oe) { // a refusal that is not about the file (busy)
				return nil, oe
			}
			return nil, ops.Invalid("pdf", err.Error())
		}
		in, err := intakeFiles(fs, false)
		if err != nil {
			return nil, ops.Invalid("pdf", err.Error())
		}
		in.apply(pre)
		return in, nil
	}
	if len(s.text) > 0 {
		if len(s.files) > 0 || len(s.zip) > 0 || len(s.docx) > 0 {
			return nil, ops.Invalid("text", "Send a text file, or a .docx, or files, or a zip: one of them.")
		}
		fs, pre, err := textToFiles(s.text, s.name, strings.TrimPrefix(s.headings, "auto"), s.conv.cutOpts())
		if err != nil {
			return nil, cutErrorIn("text", err)
		}
		in, err := intakeFiles(fs, false)
		if err != nil {
			return nil, ops.Invalid("text", err.Error())
		}
		in.apply(pre)
		return in, nil
	}
	if len(s.docx) > 0 {
		if len(s.files) > 0 || len(s.zip) > 0 {
			return nil, ops.Invalid("docx", "Send a .docx, or files, or a zip: one of them.")
		}
		fs, pre, err := docxToFiles(s.docx, s.name, strings.TrimPrefix(s.headings, "auto"), s.changes, s.conv.cutOpts())
		if err != nil {
			return nil, cutError(err)
		}
		in, err := intakeFiles(fs, false)
		if err != nil {
			return nil, ops.Invalid("docx", err.Error())
		}
		in.apply(pre)
		return in, nil
	}
	switch layout {
	case LayoutScrivener, LayoutWordPress, LayoutGhost:
		return s.convertIntake(layout)
	}
	fs, err := s.list()
	if err != nil {
		return nil, err
	}
	var pre *prepass
	if s.layout == Obsidian {
		fs, pre = obsidianConvert(fs)
	}
	// A zipped folder carries its own name as a wrapper; a listing of files is
	// already relative to the folder, so a lone subfolder there is a real part.
	in, err := intakeFiles(fs, len(s.zip) > 0)
	if err != nil {
		return nil, ops.Invalid("files", err.Error())
	}
	in.apply(pre)
	return in, nil
}

func (s source) options(edits []Edit) Options {
	o := Options{Name: s.name, SplitScenes: s.split, Layout: s.layout, DocxHeadings: s.headings, Sheets: s.conv.scrivSheets}
	for _, e := range edits {
		if e.Split != nil {
			if o.SplitFiles == nil {
				o.SplitFiles = map[string]bool{}
			}
			o.SplitFiles[e.Key] = *e.Split
		}
	}
	return o
}

func previewNew(c *ops.Ctx, in PreviewIn) (PreviewOut, error) {
	if in.StageID != "" {
		if err := onlyStage(in); err != nil {
			return PreviewOut{}, err
		}
		return stagedPreview(c, in.StageID, "")
	}
	out, err := preview(source{sum: in.sum, name: in.Name, split: in.SplitScenes, files: in.Files, zip: in.Zip, docx: in.Docx, epub: in.Epub, pdf: in.Pdf, text: in.Text, pandoc: in.Pandoc, pandocFmt: in.PandocFormat, pandocSvc: pandocOf(c), layout: layoutOf(in.Layout), headings: in.DocxHeadings, changes: in.DocxChanges, conv: in.conv(), ctx: c}, in.Edits, in.Reorder, altstore.LimitsOf(c.Env))
	if err == nil {
		out.Elsewhere = importedElsewhere(c, out.srcHash)
	}
	return out, err
}

func previewInto(c *ops.Ctx, in AppendPreviewIn) (PreviewOut, error) {
	if in.StageID != "" {
		if err := onlyStage(in); err != nil {
			return PreviewOut{}, err
		}
		return stagedPreview(c, in.StageID, in.ProjectID)
	}
	if len(in.Epub) > 0 {
		// The apply would refuse a project that already holds as many editions as it keeps: say so now.
		var n int
		if err := c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM editions WHERE project_id = ?`, in.ProjectID).Scan(&n); err != nil {
			return PreviewOut{}, ops.Internal(err)
		}
		if n >= editions.MaxPerProject {
			return PreviewOut{}, ops.Conflict(fmt.Sprintf("This project already holds %d editions, which is the most it keeps.", n))
		}
	}
	out, err := preview(source{sum: in.sum, name: in.Name, split: in.SplitScenes, files: in.Files, zip: in.Zip, docx: in.Docx, epub: in.Epub, pdf: in.Pdf, text: in.Text, pandoc: in.Pandoc, pandocFmt: in.PandocFormat, pandocSvc: pandocOf(c), layout: layoutOf(in.Layout), headings: in.DocxHeadings, changes: in.DocxChanges, conv: in.conv(), ctx: c}, in.Edits, in.Reorder, altstore.LimitsOf(c.Env))
	if err == nil {
		out.Already = alreadyImported(c, in.ProjectID, out.srcHash)
	}
	return out, err
}

// layoutOf maps the input's layout to the one the importer reads ("auto" is none).
func layoutOf(l string) string {
	switch l {
	case Obsidian, LayoutScrivener, LayoutWordPress, LayoutGhost:
		return l
	}
	return ""
}

// Preview builds the preview of a file set. It is the whole of import.preview,
// exported so the CLI and the Git import can preview a folder they already hold.
func Preview(files []File, opt Options) (PreviewOut, error) {
	var pre *prepass
	if opt.Layout == Obsidian {
		files, pre = obsidianConvert(files)
	}
	in, err := intakeFiles(files, false)
	if err != nil {
		return PreviewOut{}, ops.Invalid("files", err.Error())
	}
	in.apply(pre)
	return previewOf(in, opt, nil, nil, alts.DefaultLimits)
}

func preview(s source, edits []Edit, reorders []Reorder, lim alts.Limits) (PreviewOut, error) {
	if err := checkMapping(edits, reorders); err != nil {
		return PreviewOut{}, err
	}
	in, err := s.intake()
	if err != nil {
		return PreviewOut{}, err
	}
	return previewOf(in, s.options(edits), edits, reorders, lim)
}

func previewOf(in *intake, opt Options, edits []Edit, reorders []Reorder, lim alts.Limits) (PreviewOut, error) {
	p := buildPlan(in, opt)
	skip, err := p.applyEdits(edits, reorders)
	if err != nil {
		return PreviewOut{}, err
	}
	nodes := p.flatten(skip)
	rs := p.flattenResearch(skip)
	out := PreviewOut{Token: in.token, Layout: p.layout, ManifestFile: p.manifestFile, Project: p.project,
		Nodes: make([]Node, 0, min(len(nodes), MaxDocuments)), Warnings: append([]string{}, p.warnings...), SkippedByReason: map[string]int{},
		Research: researchNodes(rs), ResearchTotal: len(rs)}
	if in.extras != nil && len(in.extras.pictures) > 0 {
		shown := shownPictures(nodes, rs)
		for _, p := range in.extras.pictures {
			if shown[p.n] || p.always {
				out.Pictures++
				out.PictureBytes += int64(len(p.pic.Data))
			}
		}
	}
	if opt.Sheets == SheetsEntities {
		for _, r := range rs {
			if r.kind == "note" && (r.candidate == "character" || r.candidate == "place") {
				out.Sheets++
			}
		}
	}
	if in.epub != nil {
		info := in.epub.info
		out.Epub = &info
	}
	if len(in.meta) > 0 {
		out.Report = make(map[string]any, len(in.meta))
		for k, v := range in.meta {
			out.Report[k] = v
		}
	}
	for i, n := range nodes {
		if i < MaxDocuments { // the response is bounded whatever the plan holds
			node := n.Node
			node.FirstLine = firstLine(n.body)
			var firstRow string
			node.Tables, node.Cells, firstRow = tableStats(n.body)
			if node.FirstLine == "" { // a document of tables only: its first row is its first line
				node.FirstLine = firstRow
			}
			if n.Ordinal > 0 {
				node.LastLine = lastLine(n.body)
			}
			// A comment that cannot be used (with the configured limits) is said so here.
			if msg := altstore.CheckComment(n.body, lim); msg != "" {
				node.Warnings = append(append([]string{}, node.Warnings...), commentWarning(msg))
			}
			out.Nodes = append(out.Nodes, node)
		}
		out.Words += n.Words
	}
	out.Documents = len(nodes)
	out.srcHash = in.srcHashOf()
	out.Breaks = in.breaks
	for _, n := range p.autoLeft {
		out.LeftOut = append(out.LeftOut, LeftOutNode{Key: n.Key, Title: n.Title, Kind: n.Kind, Words: n.Words, Why: n.leftWhy})
	}
	out.Suggestions, out.Comments = in.tracked.countFor(nodes)
	out.SkippedTotal = len(p.skipped)
	out.Skipped = p.skipped
	if len(out.Skipped) > maxListedSkips {
		out.Skipped = out.Skipped[:maxListedSkips]
	}
	if out.Skipped == nil {
		out.Skipped = []Skipped{}
	}
	for _, s := range p.skipped {
		out.SkippedByReason[s.Reason]++
	}
	if out.Documents > MaxDocuments {
		out.Warnings = append(out.Warnings, fmt.Sprintf("This would create %d documents; the limit is %d. Do not split by heading, or import fewer files.", out.Documents, MaxDocuments))
	}
	return out, nil
}

// ---- apply ---------------------------------------------------------------------------

// failAfter lets a test fail an apply part-way, to prove nothing is kept.
var failAfter func(inserted int) error

func applyNew(c *ops.Ctx, in ApplyIn) (ApplyOut, error) {
	if in.StageID != "" {
		return applyStagedNew(c, in)
	}
	return applyNewWith(c, in, nil)
}

func applyNewWith(c *ops.Ctx, in ApplyIn, prov *Provenance) (ApplyOut, error) {
	s := source{draft: in.draft, sum: in.sum, name: in.Name, split: in.SplitScenes, files: in.Files, zip: in.Zip, docx: in.Docx, epub: in.Epub, pdf: in.Pdf, text: in.Text, pandoc: in.Pandoc, pandocFmt: in.PandocFormat, pandocSvc: pandocOf(c), layout: layoutOf(in.Layout), headings: in.DocxHeadings, changes: in.DocxChanges, conv: in.conv(), prov: prov, ctx: c}
	pl, nodes, intk, skip, err := s.resolve(in.Token, in.Edits, in.Reorder)
	if err != nil {
		return ApplyOut{}, err
	}
	rs := pl.flattenResearch(skip)
	ci := projects.CreateIn{Title: pick(in.Title, pl.project.Title), Kind: pick(in.Kind, pl.project.Kind), Subtitle: pl.project.Subtitle,
		AuthorName: pick(in.Author, pl.project.Author), Language: pick(in.Language, pl.project.Language)}
	if err := ci.Validate(); err != nil {
		return ApplyOut{}, err
	}
	out := ApplyOut{Title: ci.Title, Documents: len(nodes), Warnings: append([]string{}, pl.warnings...), SkippedTotal: len(pl.skipped)}
	pid := db.NewID("prj")
	// Pictures get their addresses before the bodies are hashed.
	pics, err := preparePictures(c, intk, pid, nodes, rs, &out)
	if err != nil {
		return ApplyOut{}, err
	}
	committed := false
	var edn *editions.Staged
	defer func() { pics.done(committed); edn.Done(committed) }()
	if edn, err = stageEdition(c, intk, pid, s.epub); err != nil {
		return ApplyOut{}, err
	}
	s.staged, s.label, s.pics = edn, in.EditionLabel, pics
	body := prepareAll(nodes, altstore.LimitsOf(c.Env))
	out.Warnings = append(out.Warnings, commentWarnings(nodes, body)...)
	var lw linkWork
	pics.lock()
	edn.Lock()
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		lw = linkWork{} // a retried transaction starts again
		now := db.Now()
		slug, err := projects.InsertTx(tx, projects.NewProject{ID: pid, OwnerID: c.P.UserID, Now: now, CreateIn: ci})
		if err != nil {
			return err
		}
		out.Slug, out.ProjectID = slug, pid
		if err := pics.commit(c, tx); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "project.created", ProjectID: pid, Payload: map[string]any{"kind": ci.Kind}})
		c.Audit(ops.AuditEntry{Action: "project.create", TargetType: "project", TargetID: pid})
		if in.UseCover {
			if err := useBookCover(c, tx, intk, pics, pid, ci.Title, &out); err != nil {
				return err
			}
		}
		if err := insertAll(c, tx, pid, "", 0, s, pl, intk, nodes, rs, body, now, &out, &lw); err != nil {
			return err
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		return ApplyOut{}, err
	}
	committed = true
	pics.done(true)
	edn.Done(true)
	lw.finish(c, pid, &out)
	return out, nil
}

func appendInto(c *ops.Ctx, in AppendIn) (ApplyOut, error) {
	if in.StageID != "" {
		return applyStagedInto(c, in)
	}
	return appendWith(c, in)
}

func appendWith(c *ops.Ctx, in AppendIn) (ApplyOut, error) {
	s := source{draft: in.draft, sum: in.sum, name: in.Name, split: in.SplitScenes, files: in.Files, zip: in.Zip, docx: in.Docx, epub: in.Epub, pdf: in.Pdf, text: in.Text, pandoc: in.Pandoc, pandocFmt: in.PandocFormat, pandocSvc: pandocOf(c), layout: layoutOf(in.Layout), headings: in.DocxHeadings, changes: in.DocxChanges, conv: in.conv(), ctx: c}
	pl, nodes, intk, skip, err := s.resolve(in.Token, in.Edits, in.Reorder)
	if err != nil {
		return ApplyOut{}, err
	}
	rs := pl.flattenResearch(skip)
	out := ApplyOut{ProjectID: in.ProjectID, Documents: len(nodes), Warnings: append([]string{}, pl.warnings...), SkippedTotal: len(pl.skipped)}
	pics, err := preparePictures(c, intk, in.ProjectID, nodes, rs, &out)
	if err != nil {
		return ApplyOut{}, err
	}
	committed := false
	var edn *editions.Staged
	defer func() { pics.done(committed); edn.Done(committed) }()
	if edn, err = stageEdition(c, intk, in.ProjectID, s.epub); err != nil {
		return ApplyOut{}, err
	}
	s.staged, s.label, s.pics = edn, in.EditionLabel, pics
	body := prepareAll(nodes, altstore.LimitsOf(c.Env))
	out.Warnings = append(out.Warnings, commentWarnings(nodes, body)...)
	var lw linkWork
	pics.lock()
	edn.Lock()
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		lw = linkWork{}
		now := db.Now()
		// The registry refused a project that was in the trash when the call began; one that went there since (an import
		// takes a while to prepare) is refused here, in the transaction that would write into it.
		var trashed int
		if err := tx.QueryRow(`SELECT count(*) FROM projects WHERE id = ? AND trashed_at IS NOT NULL`, in.ProjectID).Scan(&trashed); err != nil {
			return err
		}
		if trashed > 0 {
			return ops.ProjectInTrash()
		}
		if err := pics.commit(c, tx); err != nil {
			return err
		}
		// The new documents go after everything already in the target folder,
		// trashed or not, so a restore never lands among them.
		var last float64
		if in.ParentID != "" {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, in.ParentID, in.ProjectID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ops.Invalid("parent_id", "That folder does not exist in this project.")
			}
		}
		if err := tx.QueryRow(`SELECT COALESCE(MAX(position), 0) FROM documents WHERE project_id = ? AND COALESCE(parent_id,'') = ?`, in.ProjectID, in.ParentID).Scan(&last); err != nil {
			return err
		}
		if err := insertAll(c, tx, in.ProjectID, in.ParentID, last, s, pl, intk, nodes, rs, body, now, &out, &lw); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, now, in.ProjectID); err != nil {
			return err
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		return ApplyOut{}, err
	}
	committed = true
	pics.done(true)
	edn.Done(true)
	lw.finish(c, in.ProjectID, &out)
	return out, nil
}

// linkWork is the wiki-link work of one import. The documents are inserted
// without their links (docs.NewDoc.DeferLinks); the links waiting for the new
// titles are re-resolved once in the same transaction, and the new documents'
// own links are written after it commits, in short chunks, so a big linked
// import does not hold the single writer while other people save. The links
// are derived data: if this phase is cut short, a marker written in the import
// transaction makes the next start rebuild them.
type linkWork struct {
	titles []string
	jobs   []links.Job
}

// finish stores the links of the committed documents. It never fails the
// import, which has already committed; it says so in the warnings instead.
func (lw *linkWork) finish(c *ops.Ctx, projectID string, out *ApplyOut) {
	if len(lw.jobs) == 0 {
		return
	}
	// The import is done and committed: finish the links even if the caller leaves.
	ctx := context.WithoutCancel(c.Context)
	_, err := links.SyncBatch(ctx, c.DB(), projectID, "", lw.jobs)
	if err != nil {
		if c.Env.Log != nil {
			c.Env.Log.Warn("import: linking documents failed; links are rebuilt at the next start", "project", projectID, "err", err)
		}
		out.Warnings = append(out.Warnings, "The documents were imported, but the links between them could not all be stored now. They are rebuilt the next time the server starts.")
		return
	}
	// The explicit mentions come from the links just stored: scan again.
	if err := c.DB().WithTx(ctx, func(tx *sql.Tx) error { return mentions.MarkDirty(tx, projectID) }); err != nil && c.Env.Log != nil {
		c.Env.Log.Warn("import: could not queue the mention scan", "project", projectID, "err", err)
	}
	if err := c.DB().WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, links.PendingPrefix+projectID)
		return err
	}); err != nil && c.Env.Log != nil {
		c.Env.Log.Warn("import: could not clear the link marker", "project", projectID, "err", err)
	}
}

func pick(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// resolve rebuilds the plan from the files, checks the token, and applies the
// writer's edits. It does all the parsing, before any transaction is open.
func (s source) resolve(token string, edits []Edit, reorders []Reorder) (*plan, []*pnode, *intake, map[string]bool, error) {
	if err := checkMapping(edits, reorders); err != nil {
		return nil, nil, nil, nil, err
	}
	in, err := s.intake()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if token == "" {
		return nil, nil, nil, nil, ops.Invalid("token", "Preview the import first and send its token.")
	}
	if token != in.token {
		return nil, nil, nil, nil, ops.Conflict("These files are not the ones that were previewed. Preview them again.")
	}
	pl := buildPlan(in, s.options(edits))
	skip, err := pl.applyEdits(edits, reorders)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	nodes := pl.flatten(skip)
	switch {
	case len(nodes) == 0 && len(pl.flattenResearch(skip)) == 0:
		return nil, nil, nil, nil, ops.Invalid("files", "There is nothing to import.")
	case len(nodes) > MaxDocuments:
		return nil, nil, nil, nil, ops.Invalid("files", fmt.Sprintf("This would create %d documents; the limit is %d.", len(nodes), MaxDocuments))
	}
	return pl, nodes, in, skip, nil
}

// prepareBudget is how many bytes of Markdown may be parsed at once. Parsing a
// document costs memory out of proportion to its size (a 2 MiB file of nothing
// but headings builds hundreds of megabytes of syntax tree), so the workers share
// a budget by bytes in flight rather than counting on the core count: many small
// files run in parallel, and two large ones never do.
const prepareBudget = 2 << 20

// commentWarnings names the documents whose alternatives comment was kept as an
// ordinary comment because it could not be used (with the configured limits).
func commentWarnings(nodes []*pnode, body []docs.Prepared) []string {
	var out []string
	for i, p := range body {
		if p.Plan.Problem != "" {
			out = append(out, nodes[i].Title+": "+commentWarning(p.Plan.Problem))
		}
	}
	return out
}

// prepareAll derives every document's hash, counts and search text, across the
// available cores within prepareBudget, before the single writer is taken.
func prepareAll(nodes []*pnode, lim alts.Limits) []docs.Prepared {
	out := make([]docs.Prepared, len(nodes))
	workers := min(runtime.GOMAXPROCS(0), 8)
	var (
		mu       sync.Mutex
		cond     = sync.NewCond(&mu)
		inFlight int
		wg       sync.WaitGroup
	)
	acquire := func(n int) {
		mu.Lock()
		// A document larger than the whole budget still runs, alone.
		for inFlight > 0 && inFlight+n > prepareBudget {
			cond.Wait()
		}
		inFlight += n
		mu.Unlock()
	}
	release := func(n int) {
		mu.Lock()
		inFlight -= n
		mu.Unlock()
		cond.Broadcast()
	}
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				n := len(nodes[i].body)
				acquire(n)
				out[i] = docs.PrepareWith(nodes[i].body, lim)
				release(n)
			}
		}()
	}
	for i := range nodes {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

// insertAll writes the nodes, their provenance and their events. It runs in the
// caller's transaction; any error rolls the whole import back.
func insertAll(c *ops.Ctx, tx *sql.Tx, projectID, rootParent string, last float64, s source, pl *plan, in *intake,
	nodes []*pnode, rs []*rplan, body []docs.Prepared, now string, out *ApplyOut, lw *linkWork) error {
	sourceID := db.NewID("src")
	meta, _ := json.Marshal(map[string]any{
		"layout": pl.layout, "manifest": pl.manifestFile, "split_scenes": pl.splitLabel(s.split),
		"documents": len(nodes), "skipped": len(pl.skipped), "caps": map[string]int{"files": MaxMarkdown, "bytes": MaxTotalBytes},
	})
	kind, srcURL, srcPath, commit := "markdown", "", folderName(s.name), ""
	srcHash := in.srcHashOf()
	if in.srcKind != "" { // a .docx: the file's own hash is the provenance
		kind = in.srcKind
		var m map[string]any
		_ = json.Unmarshal(meta, &m)
		m["token"] = in.token
		for k, v := range in.meta {
			m[k] = v
		}
		meta, _ = json.Marshal(m)
	} else if in.layout == Obsidian {
		var m map[string]any
		_ = json.Unmarshal(meta, &m)
		for k, v := range in.meta {
			m[k] = v
		}
		meta, _ = json.Marshal(m)
	}
	if p := s.prov; p != nil {
		kind, srcURL, commit = p.Kind, p.URL, p.Commit
		if p.Path != "" {
			srcPath = p.Path
		}
		var m map[string]any
		_ = json.Unmarshal(meta, &m)
		for k, v := range p.Meta {
			m[k] = v
		}
		meta, _ = json.Marshal(m)
	}
	if s.draft != "" { // a staged apply says which draft it was, in the transaction that commits it
		var m map[string]any
		_ = json.Unmarshal(meta, &m)
		m["draft_id"] = s.draft
		meta, _ = json.Marshal(m)
	}
	if _, err := tx.Exec(`INSERT INTO project_sources(id, project_id, kind, source_url, source_path, source_hash, commit_sha, metadata_json, imported_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		sourceID, projectID, kind, srcURL, srcPath, srcHash, commit, string(meta), now); err != nil {
		return err
	}
	out.SourceID = sourceID
	ids := map[string]string{}
	var sceneRefs []scenes.ImportedRef // entity references a version 3 manifest carries
	count := map[string]int{}          // children placed so far, by parent key
	for i, n := range nodes {
		parent := rootParent
		if n.ParentKey != "" {
			parent = ids[n.ParentKey]
		}
		pos := float64(count[n.ParentKey]+1) * db.PositionGap
		if n.ParentKey == "" {
			pos += last
		}
		count[n.ParentKey]++
		id, err := docs.InsertTx(tx, docs.NewDoc{ProjectID: projectID, ParentID: parent, Kind: n.Kind, Status: n.Status,
			Title: n.Title, Position: pos, Body: body[i], By: c.P.UserID,
			Synopsis: n.meta.synopsis, SynopsisLong: n.meta.synopsisLong, Pov: n.meta.pov, Location: n.meta.location,
			StoryDate: n.meta.storyDate, StoryEnd: n.meta.storyEnd, TargetWords: n.meta.target, ExcludeFromCompile: n.meta.exclude, DeferLinks: true}, now)
		if err != nil {
			return err
		}
		lw.titles = append(lw.titles, n.Title)
		if strings.Contains(body[i].Content, "/files/att_") { // the pictures it shows (stored above in this transaction)
			if err := attachments.SyncRefs(tx, attachments.RefDoc, id, attachments.OwnerProject, projectID, body[i].Content); err != nil {
				return err
			}
		}
		if n.meta.povEntity != "" {
			sceneRefs = append(sceneRefs, scenes.ImportedRef{DocumentID: id, Field: "pov", EntityID: n.meta.povEntity, Text: n.meta.pov})
		}
		if n.meta.locationEntity != "" {
			sceneRefs = append(sceneRefs, scenes.ImportedRef{DocumentID: id, Field: "location", EntityID: n.meta.locationEntity, Text: n.meta.location})
		}
		if len(body[i].Links) > 0 {
			lw.jobs = append(lw.jobs, links.Job{DocumentID: id, Hash: body[i].Hash, Links: body[i].Links, Fresh: true})
		}
		ids[n.Key] = id
		if i == 0 {
			out.FirstDocumentID = id
		}
		// Every document the import made has its row: a folder that becomes an empty part has no file (an empty source_path), and is
		// recorded all the same, so that taking the import back can take the part with its chapters.
		if _, err := tx.Exec(`INSERT INTO document_sources(document_id, source_id, source_path, source_order, source_hash, stored_hash) VALUES (?,?,?,?,?,?)`,
			id, sourceID, n.SourcePath, n.order, n.hash, body[i].Hash); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "doc.created", ProjectID: projectID, DocumentID: id, Payload: map[string]any{"kind": n.Kind, "words": body[i].Words}})
		out.Words += body[i].Words
		if failAfter != nil {
			if err := failAfter(i + 1); err != nil {
				return err
			}
		}
	}
	if err := recordEdition(c, tx, s, in, pl, projectID, sourceID, ids, now, out); err != nil {
		return err
	}
	if err := writeTracked(c, tx, projectID, in.tracked, nodes, ids, out); err != nil {
		return err
	}
	if s.conv.scrivSheets == SheetsEntities {
		var err error
		if rs, err = sheetsToEntities(c, tx, projectID, out.Title, out.Slug != "", rs, now, out); err != nil {
			return err
		}
	}
	nres, err := insertResearch(tx, projectID, c.P.UserID, now, rs)
	if err != nil {
		return err
	}
	out.ResearchNotes = nres
	// Entity references from a manifest: each is checked against the project's
	// scope and the importer's own access before it is believed.
	if err := scenes.LinkImported(c, tx, vocab.ReaderOf(c.P), projectID, sceneRefs); err != nil {
		return err
	}
	// Links elsewhere that were waiting for these titles, once for the batch;
	// and, when there are links to write after the commit, a marker that makes a
	// start repair them if that phase never finishes.
	if err := links.Reresolve(tx, projectID, lw.titles...); err != nil {
		return err
	}
	// The mention index is owed a scan of the new documents; the worker reads
	// their stored text outside the writer, in short chunks.
	if err := mentions.MarkDirty(tx, projectID); err != nil {
		return err
	}
	if len(lw.jobs) > 0 {
		if _, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			links.PendingPrefix+projectID, projectID, now); err != nil {
			return err
		}
	}
	// The state each document was left in, for import.undo, after everything the import writes to it or about it is written.
	if err := recordStates(c, tx, sourceID); err != nil {
		return err
	}
	c.Emit(ops.Event{Type: "import.applied", ProjectID: projectID, Payload: map[string]any{"documents": len(nodes), "words": out.Words, "layout": pl.layout, "research_notes": nres}})
	c.Audit(ops.AuditEntry{Action: "import.apply", TargetType: "project", TargetID: projectID,
		Detail: map[string]any{"source_id": sourceID, "layout": pl.layout, "documents": len(nodes), "skipped": len(pl.skipped), "research_notes": nres}})
	return nil
}

func (p *plan) splitLabel(s string) string {
	if s == "h2" {
		return "h2"
	}
	return "none"
}

// folderName is the name recorded as the source: a folder's base name, never a
// path from the caller's machine.
func folderName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(strings.TrimRight(name, "/"))
	if name == "." || name == "/" || name == "" {
		return ""
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}
