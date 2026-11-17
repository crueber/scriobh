package pdfread

import "time"

// Limits bounds what one file may cost. The zero value is replaced by
// DefaultLimits field by field, so a test can lower one number.
type Limits struct {
	FileBytes      int           // the file
	Objects        int           // objects in the file (declared, scanned, or inside object streams)
	Pages          int           // pages in the page tree
	Depth          int           // nesting of arrays and dictionaries, and of resolving references
	TreeNodes      int           // nodes visited in the page tree
	StreamBytes    int64         // one stream, inflated
	InflateBytes   int64         // every stream of the file together, inflated
	Ratio          int           // inflated size over stored size, for a stream inflating past RatioFloor
	RatioFloor     int64         // below this many bytes the ratio is not asked
	OpsPerPage     int           // operators interpreted in one page (and the forms it draws)
	OpsTotal       int           // operators interpreted in the file
	TextBytes      int           // bytes of text kept in all
	FormDepth      int           // nesting of form XObjects
	Fonts          int           // fonts read
	CMapEntries    int           // entries kept of one font's character map
	RebuildObjects int           // objects accepted when the cross-reference table is rebuilt by scanning
	OutlineItems   int           // outline entries read
	NodeBudget     int           // nodes visited in name trees, destination trees and the outline, for the whole file
	ParseMem       int64         // estimated memory of the objects (not stream data) parsed, for the whole file
	WorkBudget     int           // elements of arrays visited in name trees, destination trees and page trees, for the whole file
	CMapTotal      int           // bytes of the entries of every character map together (the length of its text and 32 for each)
	WidthCells     int           // glyph widths read from every font together
	Pieces         int           // pieces of text kept, for the whole file
	ContentBytes   int64         // bytes of content streams interpreted, for the whole file (a form drawn twice is read twice)
	Time           time.Duration // wall time for reading all pages; 0 means none
}

// DefaultLimits are the limits the importer uses. A 300-page novel is well
// inside every one of them.
var DefaultLimits = Limits{
	FileBytes:      64 << 20,
	Objects:        1_000_000,
	Pages:          3000,
	Depth:          48,
	TreeNodes:      20_000,
	StreamBytes:    32 << 20,
	InflateBytes:   384 << 20,
	Ratio:          30,
	RatioFloor:     512 << 10,
	OpsPerPage:     600_000,
	OpsTotal:       30_000_000,
	TextBytes:      48 << 20,
	FormDepth:      6,
	Fonts:          3000,
	CMapEntries:    100_000,
	RebuildObjects: 1_000_000,
	OutlineItems:   20_000,
	NodeBudget:     50_000,
	ParseMem:       32 << 20,
	WorkBudget:     2_000_000,
	CMapTotal:      24 << 20,
	WidthCells:     400_000,
	Pieces:         150_000,
	ContentBytes:   256 << 20,
}

func (l Limits) fill() Limits {
	d := DefaultLimits
	if l.FileBytes > 0 {
		d.FileBytes = l.FileBytes
	}
	if l.Objects > 0 {
		d.Objects = l.Objects
	}
	if l.Pages > 0 {
		d.Pages = l.Pages
	}
	if l.Depth > 0 {
		d.Depth = l.Depth
	}
	if l.TreeNodes > 0 {
		d.TreeNodes = l.TreeNodes
	}
	if l.StreamBytes > 0 {
		d.StreamBytes = l.StreamBytes
	}
	if l.InflateBytes > 0 {
		d.InflateBytes = l.InflateBytes
	}
	if l.Ratio > 0 {
		d.Ratio = l.Ratio
	}
	if l.RatioFloor > 0 {
		d.RatioFloor = l.RatioFloor
	}
	if l.OpsPerPage > 0 {
		d.OpsPerPage = l.OpsPerPage
	}
	if l.OpsTotal > 0 {
		d.OpsTotal = l.OpsTotal
	}
	if l.TextBytes > 0 {
		d.TextBytes = l.TextBytes
	}
	if l.FormDepth > 0 {
		d.FormDepth = l.FormDepth
	}
	if l.Fonts > 0 {
		d.Fonts = l.Fonts
	}
	if l.CMapEntries > 0 {
		d.CMapEntries = l.CMapEntries
	}
	if l.RebuildObjects > 0 {
		d.RebuildObjects = l.RebuildObjects
	}
	if l.OutlineItems > 0 {
		d.OutlineItems = l.OutlineItems
	}
	if l.NodeBudget > 0 {
		d.NodeBudget = l.NodeBudget
	}
	if l.ParseMem > 0 {
		d.ParseMem = l.ParseMem
	}
	if l.WorkBudget > 0 {
		d.WorkBudget = l.WorkBudget
	}
	if l.CMapTotal > 0 {
		d.CMapTotal = l.CMapTotal
	}
	if l.WidthCells > 0 {
		d.WidthCells = l.WidthCells
	}
	if l.Pieces > 0 {
		d.Pieces = l.Pieces
	}
	if l.ContentBytes > 0 {
		d.ContentBytes = l.ContentBytes
	}
	d.Time = l.Time
	return d
}
