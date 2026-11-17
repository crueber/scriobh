package imports

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// Reading what the converters read: a zip or a listing of files that is not
// Markdown. Everything the folder importer does for a zip is done here too: the
// directory is walked before the archive is opened, an entry's declared size is
// never trusted, a symbolic link is not followed, and there is a limit on each
// file and on all of them together.

const (
	maxScrivEntries = 40_000   // entries in a zipped .scriv (several files for every document)
	maxScrivFile    = 32 << 20 // one RTF file (the reader does not buffer a picture)
	maxScrivIndex   = 32 << 20 // the binder (.scrivx)
	maxScrivSmall   = 1 << 20  // a synopsis
	maxScrivTotal   = 96 << 20 // all of it, inflated
	maxExportFile   = 64 << 20 // a WordPress or Ghost export
	maxExportTotal  = 96 << 20
	maxExportZip    = 64
)

// zipWant says whether a converter reads an entry: the limit on its size, or ok=false.
type zipWant func(lower string) (limit int64, ok bool)

// readZipFor lists a zip, with the bytes of the entries want accepts. An entry
// that is too big, or that the archive lies about, is listed by size only, as
// the folder importer lists what it will not read.
func readZipFor(data []byte, maxEntries int, maxTotal int64, want zipWant) ([]File, error) {
	if err := checkZipDirectoryN(data, maxEntries); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("that is not a readable zip file")
	}
	if len(zr.File) > maxEntries {
		return nil, fmt.Errorf("the zip holds %d entries; the limit is %d", len(zr.File), maxEntries)
	}
	out := make([]File, 0, len(zr.File))
	var scratch bytes.Buffer // the one buffer entries are read into; see below
	budget, work := maxTotal, 2*maxTotal
	// Pictures have a budget of their own, so a folder of photographs cannot crowd out the text.
	picBudget, picCount := int64(maxPictureTotal), 0
	work += 2 * picBudget
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		if zf.Mode()&fs.ModeSymlink != 0 {
			out = append(out, File{Path: zf.Name, Size: -1})
			continue
		}
		lower := strings.ToLower(zf.Name)
		limit, ok := want(lower)
		pic := ok && isPictureName(lower)
		if pic {
			if picCount++; picCount > 2*maxPictures {
				ok = false
			}
		}
		if !ok || zf.UncompressedSize64 > uint64(limit) {
			out = append(out, File{Path: zf.Name, Size: int64(min(zf.UncompressedSize64, 1<<62))})
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			out = append(out, File{Path: zf.Name, Size: int64(zf.UncompressedSize64)})
			continue
		}
		// Never allocate by what the entry says: it is read through the limit into
		// one scratch buffer that every entry shares, which grows with what really
		// comes out, so an entry that lies, or fails, or is thrown away allocates
		// nothing of its own. Only an entry that is kept is copied, to its exact size.
		// Every byte read counts against a budget of work, rejected entries included,
		// so entries that inflate to the limit and are thrown away cannot be repeated
		// for ever.
		scratch.Reset()
		_, err = scratch.ReadFrom(io.LimitReader(rc, limit+1))
		rc.Close()
		b := scratch.Bytes()
		if work -= int64(len(b)); work < 0 {
			return nil, errors.New("the zip inflates to more than the import limit")
		}
		if err != nil || int64(len(b)) > limit {
			out = append(out, File{Path: zf.Name, Size: int64(max(len(b), 1))})
			continue
		}
		if pic {
			if picBudget -= int64(len(b)); picBudget < 0 {
				out = append(out, File{Path: zf.Name, Size: int64(len(b))}) // listed, not read: the pictures are over their limit
				continue
			}
		} else if budget -= int64(len(b)); budget < 0 {
			return nil, errors.New("the zip holds more than the import limit")
		}
		out = append(out, File{Path: zf.Name, Data: append(make([]byte, 0, len(b)), b...), Size: int64(len(b))})
	}
	return out, nil
}

// zipNames lists the names in a zip's central directory, after the directory
// has been checked, without reading any entry.
func zipNames(data []byte, maxEntries int) ([]string, error) {
	if err := checkZipDirectoryN(data, maxEntries); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("that is not a readable zip file")
	}
	names := make([]string, 0, len(zr.File))
	for _, zf := range zr.File {
		if !zf.FileInfo().IsDir() {
			names = append(names, zf.Name)
		}
	}
	return names, nil
}

// scrivWant is what a Scrivener bundle is read for.
func scrivWant(lower string) (int64, bool) {
	switch {
	case strings.HasSuffix(lower, ".scrivx"):
		return maxScrivIndex, true
	case strings.HasSuffix(lower, ".rtf"):
		return maxScrivFile, true
	case strings.HasSuffix(lower, ".txt"):
		return maxScrivSmall, true
	}
	return pictureWant(lower)
}

// exportWant is what a WordPress or Ghost export zip is read for.
func exportWant(lower string) (int64, bool) {
	if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".json") {
		return maxExportFile, true
	}
	return pictureWant(lower)
}

// sniffLen is how much of a file is looked at to recognise it.
const sniffLen = 64 << 10

// detectConverted recognises a Scrivener project, a WordPress export or a Ghost
// export by its own markers, and only when there is no Markdown to read as a
// folder instead: a .scrivx file; a lone .xml that says it is a WordPress
// export; a lone .json that says it is a Ghost export. It returns "" otherwise.
func detectConverted(files []File, zipData []byte) string {
	var names []string
	if len(zipData) > 0 {
		var err error
		if names, err = zipNames(zipData, maxScrivEntries); err != nil {
			return ""
		}
	} else {
		for _, f := range files {
			names = append(names, f.Path)
		}
	}
	var scrivx, data []string
	for _, n := range names {
		l := strings.ToLower(strings.ReplaceAll(n, "\\", "/"))
		switch {
		case isMarkdown(l):
			return "" // a folder of Markdown is a folder
		case strings.HasSuffix(l, ".scrivx"):
			scrivx = append(scrivx, n)
		case (strings.HasSuffix(l, ".xml") || strings.HasSuffix(l, ".json")) && !hidden(strings.ReplaceAll(n, "\\", "/")):
			data = append(data, n)
		}
	}
	if len(scrivx) > 0 {
		return LayoutScrivener
	}
	if len(data) != 1 {
		return ""
	}
	head := sniffHead(files, zipData, data[0])
	return sniffExport(head)
}

// sniffHead reads the start of the named file from a listing or a zip.
func sniffHead(files []File, zipData []byte, name string) []byte {
	if len(zipData) > 0 {
		zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
		if err != nil {
			return nil
		}
		for _, zf := range zr.File {
			if zf.Name == name {
				rc, err := zf.Open()
				if err != nil {
					return nil
				}
				defer rc.Close()
				b, _ := io.ReadAll(io.LimitReader(rc, sniffLen))
				return b
			}
		}
		return nil
	}
	for _, f := range files {
		if f.Path == name {
			b := f.bytesOf()
			return b[:min(len(b), sniffLen)]
		}
	}
	return nil
}

// sniffExport says which export a file's first bytes belong to.
func sniffExport(head []byte) string {
	h, _ := decodeXML(append([]byte(nil), head...))
	s := string(h)
	switch {
	case strings.Contains(s, "wordpress.org/export") && strings.Contains(s, "<rss"):
		return LayoutWordPress
	case strings.Contains(s, "wp:wxr_version"):
		return LayoutWordPress
	}
	t := strings.TrimLeft(s, " \t\r\n")
	if strings.HasPrefix(t, "{") && (strings.Contains(t, `"exported_on"`) || strings.Contains(t, `"db"`) && strings.Contains(t, `"data"`)) {
		return LayoutGhost
	}
	return ""
}

// listingOf returns the files a converter reads, from a listing or a zip.
func listingFor(files []File, zipData []byte, maxEntries int, maxTotal int64, want zipWant) ([]File, error) {
	if len(zipData) > 0 {
		if len(files) > 0 {
			return nil, errors.New("send files or a zip, not both")
		}
		return readZipFor(zipData, maxEntries, maxTotal, want)
	}
	if len(files) > maxEntries {
		return nil, fmt.Errorf("the listing holds %d files; the limit is %d", len(files), maxEntries)
	}
	return files, nil
}

// MaxScrivenerEntries is the most files a Scrivener project may be made of.
const MaxScrivenerEntries = maxScrivEntries

// WantFile says whether the files of a folder are read for a layout, and up to
// what size: what the command line reads from a local folder. A layout that is
// not one of the converted sources reads Markdown and manifests, as before.
func WantFile(layout, lower string) (limit int64, ok bool) {
	switch layout {
	case LayoutScrivener:
		return scrivWant(lower)
	case LayoutWordPress, LayoutGhost:
		return exportWant(lower)
	}
	return 0, false
}

// ExportFileLimit is the largest WordPress or Ghost export file that is read.
const ExportFileLimit = maxExportFile
