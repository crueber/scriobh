// Package pdfread reads the text layer of a PDF with the standard library
// only. It is written for files somebody else made: every size, count, depth
// and expansion ratio is limited (limits.go), nothing in a file is executed,
// opened or extracted (JavaScript and launch actions are counted and ignored,
// embedded files are counted and never read), an encrypted file is refused
// whatever its password is, and a damaged file is read as far as it can be
// (a cross-reference table that cannot be trusted is rebuilt by scanning).
//
// What it recovers is positioned text: for each page, runs of characters with
// where they sit, how large they are and whether the font is bold, italic or
// monospaced, the outline (bookmarks), the document's own title and author,
// and the places where pictures are drawn (counted, never decoded). Turning
// that into paragraphs and chapters is the caller's job (layout.go does it).
package pdfread

import "errors"

// Errors a caller can tell apart.
var (
	ErrNotPDF    = errors.New("that is not a PDF file")
	ErrEncrypted = errors.New("that PDF is encrypted (password protected); it is not read, and no password is tried")
	ErrDamaged   = errors.New("that PDF is damaged beyond what can be read")
	ErrTooLarge  = errors.New("that PDF is larger than the import limit")
	ErrNoPages   = errors.New("that PDF has no pages")
)
