// Package imports turns a folder of Markdown into a project, or into new
// documents at the end of an existing one.
//
// The importer works on an in-memory file tree, never on the disk: callers
// (the CLI reading a folder, the UI posting a zip, the Git import that clones
// to a temp directory) hand it []File and get the same plan back. A folder is
// read as one of two layouts:
//
//   - A Scríobh mirror or export: a scriobh.json (the export bundle and the Git
//     layout) or project.json (the mirror) at the root names the project, and the
//     numbered manuscript/ tree gives the structure.
//   - A generic Markdown folder: folders become parts, NN-title.md files become
//     chapters, and, when asked, the second-level headings of a file become scenes.
//
// There is no Hugo, Jekyll or Obsidian layout detection: a folder is never
// guessed to be anything but those two. Other sources are converted first into
// the same shape, and chosen by the writer or recognised by their own file
// markers when there is no Markdown to read: a Word document (docxconv.go), an
// Obsidian vault (obsidian.go, only when asked for), a Scrivener project
// (scrivener.go), a WordPress export (wordpress.go) and a Ghost export
// (ghost.go). A converter of the last three makes a Scríobh layout with a
// manifest of its own (convert.go), so titles, kinds, statuses, synopses and the
// compile flag reach the plan, and research notes beside it (research.go).
// Preview writes nothing; apply re-sends the files with the same token and
// creates everything in one transaction or nothing at all.
package imports

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Caps. A folder over any of these is refused or has the offending files
// skipped and reported, never silently truncated.
const (
	MaxListed       = 10_000    // entries in the listing, including skipped ones
	MaxMarkdown     = 2_000     // Markdown files that become documents
	MaxDocuments    = 5_000     // documents after splitting by heading
	MaxFileBytes    = 2 << 20   // one file, matching the document body cap
	MaxTotalBytes   = 32 << 20  // all accepted Markdown together
	MaxPathBytes    = 512       // one path
	MaxDepth        = 10        // directories deep
	MaxManifest     = 8 << 20   // scriobh.json or project.json (a mirror lists every document with its synopses)
	MaxBody         = 48 << 20  // the request body the import ops accept (base64 and JSON overhead included)
	maxZipEntries   = MaxListed // entries read from an uploaded zip
	maxZipDirectory = 8 << 20   // bytes of central directory (10,000 entries with long names fit)
	maxZipInflated  = MaxTotalBytes + MaxManifest*2
)

// File is one entry of the folder being imported.
//
// Send text in Data (base64 on the wire) so the server sees the bytes as they
// are and can report a file that is not valid UTF-8. Content is a convenience
// for text only (an agent posting a few chapters): JSON turns invalid bytes
// into U+FFFD before the server sees them, so a binary file must go in Data.
// A file you do not want to send (an image, something over the size cap) is
// listed with its Size and no bytes, and is reported as skipped.
type File struct {
	Path    string `json:"path" desc:"Path relative to the folder, with forward slashes."`
	Data    []byte `json:"data,omitempty" desc:"The file's bytes, base64."`
	Content string `json:"content,omitempty" desc:"The file as UTF-8 text; use data for anything that might not be."`
	Size    int64  `json:"size,omitempty" desc:"Size in bytes of a file whose bytes are not sent; -1 marks something that is not a regular file."`
}

// Skipped is a file that will not be imported, and why.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Reasons a file is skipped.
const (
	reasonHidden    = "hidden file or folder"
	reasonNotMD     = "not a Markdown file"
	reasonTooLarge  = "larger than the 2 MiB file limit"
	reasonUTF8      = "not valid UTF-8 text"
	reasonUnsafe    = "unsafe path"
	reasonLongPath  = "path too long"
	reasonDeep      = "nested too deeply"
	reasonDuplicate = "duplicate path"
	reasonUnsent    = "contents were not sent"
	reasonNotBook   = "outside the manuscript"
	reasonReadme    = "generated README"
	reasonSymlink   = "symbolic link"
	reasonTooMany   = "too many headings to split into scenes"
	reasonManifest  = "manifest larger than the limit"
)

// srcFile is an accepted Markdown file.
type srcFile struct {
	Path string // normalised, root-stripped
	Text string // BOM removed, line endings LF
	Hash string // sha256 of the bytes as read
}

// intake is the filtered file set.
type intake struct {
	md       []*srcFile
	manifest map[string][]byte // scriobh.json / project.json at the root, by name
	// bigManifest names a manifest that was over MaxManifest and was left out,
	// so the plan can say so instead of quietly reading the folder as generic.
	bigManifest []string
	skipped     []Skipped
	token       string
	total       int
	// From a converter (DOCX, Obsidian): the layout the plan reports, where the
	// files came from, the project facts the source states, and what the
	// conversion left out.
	layout, srcKind, srcHash string
	project                  *ProjectInfo
	warnings                 []string
	meta                     map[string]any
	// From a converter of Scrivener, WordPress or Ghost: research notes to
	// write beside the documents, and the manifest it made (kept, where a DOCX
	// or Obsidian conversion's folder is read as a plain one).
	extras       *extras
	keepManifest bool
	// From the EPUB converter: the book as read, for its preview and its edition record. Read only.
	epub    *epubData
	tracked *trackedSet // a Word file's tracked changes and comments
	// From the Word converter: what the chapter detector says of each file it made, by path, and how the book was cut.
	notes  map[string]*docNote
	breaks *BreakInfo
}

// prepass is what a converter (DOCX, Obsidian) reports about the files it made.
type prepass struct {
	layout, srcKind, srcHash string
	project                  *ProjectInfo
	warnings                 []string
	skipped                  []Skipped
	meta                     map[string]any
	extras                   *extras
	keepManifest             bool
	epub                     *epubData
	tracked                  *trackedSet // a Word file's changes and comments, to write after the documents
	tokenMix                 string      // what the token must also cover
	notes                    map[string]*docNote
	breaks                   *BreakInfo
}

// apply puts a converter's report on the intake.
func (in *intake) apply(p *prepass) {
	if p == nil {
		return
	}
	in.layout, in.srcKind, in.srcHash, in.project, in.meta = p.layout, p.srcKind, p.srcHash, p.project, p.meta
	in.warnings = append(in.warnings, p.warnings...)
	in.skipped = append(in.skipped, p.skipped...)
	in.extras, in.keepManifest = p.extras, p.keepManifest
	in.epub = p.epub
	if p.epub != nil {
		// The book's own hash is part of what was previewed: another book with the same text is another edition.
		sum := sha256.Sum256([]byte(in.token + "\x00epub\x00" + p.epub.sha))
		in.token = "imp_" + hex.EncodeToString(sum[:])[:32]
	}
	in.tracked = p.tracked
	in.notes, in.breaks = p.notes, p.breaks
	if p.tokenMix != "" {
		sum := sha256.Sum256([]byte(in.token + "\x00" + p.tokenMix))
		in.token = "imp_" + hex.EncodeToString(sum[:])[:32]
	}
	if p.extras != nil {
		// What is written besides the files is part of what was previewed.
		sum := sha256.Sum256([]byte(in.token + "\x00" + p.extras.hash))
		in.token = "imp_" + hex.EncodeToString(sum[:])[:32]
	}
}

// bytesOf returns a file's bytes: Data when set, else Content.
func (f File) bytesOf() []byte {
	if len(f.Data) > 0 {
		return f.Data
	}
	return []byte(f.Content)
}

// ReadZip lists the files of a zip upload. It reads at most maxZipEntries
// entries and maxZipInflated bytes of content in total, whatever the archive
// claims, and never trusts an entry's declared size.
func ReadZip(data []byte) ([]File, error) {
	if err := checkZipDirectory(data); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("That is not a readable zip file.")
	}
	if len(zr.File) > maxZipEntries {
		return nil, fmt.Errorf("the zip holds %d entries; the limit is %d", len(zr.File), maxZipEntries)
	}
	var out []File
	var budget int64 = maxZipInflated
	work := 2 * budget
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		if zf.Mode()&fs.ModeSymlink != 0 { // a symbolic link stored in the archive
			out = append(out, File{Path: zf.Name, Size: -1})
			continue
		}
		name := strings.ToLower(zf.Name)
		wanted := strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".markdown") || strings.HasSuffix(name, ".json")
		// A manifest lists every document with its synopses, so it may be larger
		// than a document body.
		limit := int64(MaxFileBytes)
		if base := path.Base(name); base == "scriobh.json" || base == "project.json" {
			limit = MaxManifest
		}
		if !wanted || zf.UncompressedSize64 > uint64(limit) {
			out = append(out, File{Path: zf.Name, Size: int64(min(zf.UncompressedSize64, 1<<62))})
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			out = append(out, File{Path: zf.Name, Size: int64(zf.UncompressedSize64)})
			continue
		}
		b, err := io.ReadAll(io.LimitReader(rc, limit+1))
		rc.Close()
		if work -= int64(len(b)); work < 0 { // entries that inflate past their limit are work too
			return nil, errors.New("the zip inflates to more than the import limit")
		}
		if err != nil {
			out = append(out, File{Path: zf.Name, Size: int64(zf.UncompressedSize64)})
			continue
		}
		if int64(len(b)) > limit { // the archive lied about its size
			out = append(out, File{Path: zf.Name, Size: int64(len(b))})
			continue
		}
		budget -= int64(len(b))
		if budget < 0 {
			return nil, errors.New("the zip holds more text than the import limit")
		}
		out = append(out, File{Path: zf.Name, Data: b, Size: int64(len(b))})
	}
	return out, nil
}

// checkZipDirectory refuses an archive that archive/zip would spend memory on.
//
// The declared entry count and directory size cannot be trusted: archive/zip
// starts at the declared offset and reads directory headers until one does not
// match, whatever the end record says, and allocates for each. So this walks
// the same chain of headers itself (cheaply: it reads lengths, not names), from
// each place the library might start, and refuses when it is longer than the
// entry limit. Anything that would send the library to a zip64 record is
// refused outright.
func checkZipDirectory(data []byte) error { return checkZipDirectoryN(data, maxZipEntries) }

// checkZipDirectoryN is checkZipDirectory with the entry limit a caller sets (a
// Scrivener bundle holds several files for every document).
func checkZipDirectoryN(data []byte, limit int) error {
	const eocd = 22
	bad := errors.New("That is not a readable zip file.")
	if len(data) < eocd {
		return bad
	}
	// The record sits at the end, followed only by a comment of up to 64 KiB.
	lo := max(0, len(data)-eocd-0xFFFF)
	for i := len(data) - eocd; i >= lo; i-- {
		if data[i] != 'P' || data[i+1] != 'K' || data[i+2] != 5 || data[i+3] != 6 {
			continue
		}
		entries := int(binary.LittleEndian.Uint16(data[i+10:]))
		cdSize := int64(binary.LittleEndian.Uint32(data[i+12:]))
		cdOffset := int64(binary.LittleEndian.Uint32(data[i+16:]))
		if entries == 0xFFFF || cdSize == 0xFFFFFFFF || cdOffset == 0xFFFFFFFF ||
			(i >= 20 && data[i-20] == 'P' && data[i-19] == 'K' && data[i-18] == 6 && data[i-17] == 7) {
			return errors.New("zip64 archives are not accepted; send fewer files")
		}
		if entries > limit {
			return fmt.Errorf("the zip holds %d entries; the limit is %d", entries, limit)
		}
		// Where the library may begin reading headers: the declared offset, or
		// (when the archive has data in front of it) just before the end record.
		for _, start := range []int64{cdOffset, int64(i) - cdSize} {
			if start < 0 || start >= int64(len(data)) {
				continue
			}
			if n := directoryChain(data, start, limit+1); n > limit {
				return fmt.Errorf("the zip's directory holds more than %d entries; the limit is %d", limit, limit)
			}
		}
		return nil
	}
	return bad
}

// directoryChain counts consecutive central directory headers from start,
// stopping at limit.
func directoryChain(data []byte, start int64, limit int) int {
	n := 0
	pos := start
	for n < limit && pos+46 <= int64(len(data)) {
		h := data[pos:]
		if h[0] != 'P' || h[1] != 'K' || h[2] != 1 || h[3] != 2 {
			break
		}
		pos += 46 + int64(binary.LittleEndian.Uint16(h[28:])) + int64(binary.LittleEndian.Uint16(h[30:])) + int64(binary.LittleEndian.Uint16(h[32:]))
		n++
	}
	return n
}

// cleanPath normalises a path from a listing. ok is false (with a reason) for
// anything that could escape the folder or is not a usable path.
func cleanPath(p string) (string, string) {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", reasonUnsafe
	}
	if len(p) >= 2 && p[1] == ':' && unicode.IsLetter(rune(p[0])) { // a drive letter
		return "", reasonUnsafe
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return "", reasonUnsafe
		}
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", reasonUnsafe
		}
	}
	if !utf8.ValidString(p) {
		return "", reasonUnsafe
	}
	if len(p) > MaxPathBytes {
		return "", reasonLongPath
	}
	if len(segs)-1 > MaxDepth {
		return "", reasonDeep
	}
	return path.Join(segs...), ""
}

func hidden(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if strings.HasPrefix(s, ".") || s == "__MACOSX" {
			return true
		}
	}
	return false
}

func isMarkdown(p string) bool {
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".markdown")
}

// normalise strips a byte-order mark and makes every line ending LF.
func normalise(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	s := string(b)
	if strings.ContainsRune(s, '\r') {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	return s
}

// intakeFiles filters a listing into the Markdown to import and the files to
// report. It returns an error only for a folder that is over a hard cap.
func intakeFiles(files []File, stripRoot bool) (*intake, error) {
	if len(files) > MaxListed {
		return nil, fmt.Errorf("the folder lists %d files; the limit is %d. Send only the Markdown", len(files), MaxListed)
	}
	in := &intake{manifest: map[string][]byte{}}
	seen := map[string]bool{}
	type kept struct {
		path string
		file File
	}
	var keep []kept
	var allPaths []string
	for _, f := range files {
		p, why := cleanPath(f.Path)
		switch {
		case why != "":
			in.skipped = append(in.skipped, Skipped{Path: shown(f.Path), Reason: why})
			continue
		case seen[p]:
			in.skipped = append(in.skipped, Skipped{Path: p, Reason: reasonDuplicate})
			continue
		}
		seen[p] = true
		if hidden(p) {
			in.skipped = append(in.skipped, Skipped{Path: p, Reason: reasonHidden})
			continue
		}
		allPaths = append(allPaths, p)
		keep = append(keep, kept{p, f})
	}
	root := ""
	if stripRoot {
		root = commonRoot(allPaths)
	}
	for _, k := range keep {
		p := strings.TrimPrefix(k.path, root)
		f := k.file
		lower := strings.ToLower(p)
		isManifest := (lower == "scriobh.json" || lower == "project.json") && !strings.Contains(p, "/")
		switch {
		case f.Size < 0:
			in.skipped = append(in.skipped, Skipped{Path: p, Reason: reasonSymlink})
			continue
		case !isMarkdown(p) && !isManifest:
			in.skipped = append(in.skipped, Skipped{Path: p, Reason: reasonNotMD})
			continue
		case int64(len(f.Data))+int64(len(f.Content)) == 0 && f.Size > 0:
			in.skipped = append(in.skipped, Skipped{Path: p, Reason: unsentReason(f.Size)})
			continue
		}
		b := f.bytesOf()
		if len(b) > MaxFileBytes && !isManifest {
			in.skipped = append(in.skipped, Skipped{Path: p, Reason: reasonTooLarge})
			continue
		}
		if isManifest {
			if len(b) <= MaxManifest {
				in.manifest[lower] = b
			} else {
				in.bigManifest = append(in.bigManifest, lower)
				in.skipped = append(in.skipped, Skipped{Path: p, Reason: reasonManifest})
			}
			continue
		}
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			in.skipped = append(in.skipped, Skipped{Path: p, Reason: reasonUTF8})
			continue
		}
		in.total += len(b)
		sum := sha256.Sum256(b)
		in.md = append(in.md, &srcFile{Path: p, Text: normalise(b), Hash: hex.EncodeToString(sum[:])})
	}
	if len(in.md) > MaxMarkdown {
		return nil, fmt.Errorf("the folder holds %d Markdown files; the limit is %d", len(in.md), MaxMarkdown)
	}
	if in.total > MaxTotalBytes {
		return nil, fmt.Errorf("the Markdown totals %d MB; the limit is %d MB", in.total>>20, MaxTotalBytes>>20)
	}
	sort.Slice(in.md, func(i, j int) bool { return in.md[i].Path < in.md[j].Path })
	in.token = tokenOf(in)
	return in, nil
}

func unsentReason(size int64) string {
	if size > MaxFileBytes {
		return reasonTooLarge
	}
	return reasonUnsent
}

// shown makes a rejected path safe to display: it is echoed back to the
// caller, so control characters are removed.
func shown(p string) string {
	p = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, p)
	if rs := []rune(p); len(rs) > 120 { // cut between characters, never inside one
		p = string(rs[:120]) + "..."
	}
	return p
}

// commonRoot returns "dir/" when every path lies inside one top-level folder
// (a zipped folder always does), else "". It is dropped for zip uploads only:
// a listing of files is relative to the folder already.
func commonRoot(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	first, _, ok := strings.Cut(paths[0], "/")
	if !ok {
		return ""
	}
	for _, p := range paths {
		top, rest, has := strings.Cut(p, "/")
		if !has || top != first || rest == "" {
			return ""
		}
	}
	return first + "/"
}

// tokenOf identifies the accepted file set: a hash over each file's path and
// content hash, in path order, plus the manifests. Apply refuses a file set
// whose token differs from the one previewed.
func tokenOf(in *intake) string {
	h := sha256.New()
	for _, f := range in.md {
		fmt.Fprintf(h, "%s\x00%s\n", f.Path, f.Hash)
	}
	names := make([]string, 0, len(in.manifest))
	for n := range in.manifest {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sum := sha256.Sum256(in.manifest[n])
		fmt.Fprintf(h, "%s\x00%s\n", n, hex.EncodeToString(sum[:]))
	}
	return "imp_" + hex.EncodeToString(h.Sum(nil))[:32]
}
