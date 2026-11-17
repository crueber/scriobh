package pandoc

import "sort"

// Import formats: the name a person chooses, and Pandoc's reader for it. A name
// is never passed on as typed; it selects an entry here.
var importFormats = map[string]string{
	"odt": "odt", "rtf": "rtf", "latex": "latex", "mediawiki": "mediawiki", "org": "org",
	"textile": "textile", "rst": "rst", "fb2": "fb2", "typst": "typst", "docbook": "docbook",
}

// Export formats: the name, Pandoc's writer, the file extension and its content type.
var exportFormats = map[string]struct{ writer, ext, ctype string }{
	"odt":       {"odt", "odt", "application/vnd.oasis.opendocument.text"},
	"rtf":       {"rtf", "rtf", "application/rtf"},
	"latex":     {"latex", "tex", "application/x-latex"},
	"rst":       {"rst", "rst", "text/x-rst; charset=utf-8"},
	"mediawiki": {"mediawiki", "wiki", "text/plain; charset=utf-8"},
}

// ImportFormats lists the names an import may use, sorted.
func ImportFormats() []string { return keys(importFormats) }

// ExportFormats lists the names an export may use, sorted.
func ExportFormats() []string {
	out := make([]string, 0, len(exportFormats))
	for k := range exportFormats {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidImport reports whether name is an import format.
func ValidImport(name string) bool { _, ok := importFormats[name]; return ok }

// ValidExport reports whether name is an export format.
func ValidExport(name string) bool { _, ok := exportFormats[name]; return ok }

// ExportExt is the file extension of an export format ("" if it is not one).
func ExportExt(name string) string { return exportFormats[name].ext }

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
