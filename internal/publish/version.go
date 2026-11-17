package publish

// EngineVersion names the writers' behaviour: the typesetter, the PDF writer,
// the embedded fonts and the DOCX and EPUB writers. It is part of a compile's
// input hash, so the same input hash means the same bytes: a change that can
// alter the bytes a writer produces for the same input (a layout rule, a font,
// a style) must change this string. TestPDFGolden refuses to rewrite its pinned
// hashes unless it has changed.
const EngineVersion = "typeset-3/fonts-liberation-2.1.5+dejavu-2.37/pdfw-1/docx-1/epub-1"
