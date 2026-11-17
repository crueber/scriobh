package pdfread

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

// A number from a cross-reference section that is not an offset any file can hold (here 1e20, past 2^63) must be
// refused, not turned into some platform's idea of an int64: amd64 converts such a float to MinInt64, which passed
// the "inside the file" test and put the lexer at a negative position.

const huge = "99999999999999999999" // about 1e20: the lexer has no exponent form

// classicFile is a one-page-less but well-formed file with a classic table; extra goes in the trailer, startxref
// is the value written after the keyword ("" for the true offset of the table).
func classicFile(extra, startxref string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	o1 := b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	o2 := b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")
	xr := b.Len()
	fmt.Fprintf(&b, "xref\n0 3\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n", o1, o2)
	fmt.Fprintf(&b, "trailer\n<< /Size 3 /Root 1 0 R %s >>\nstartxref\n", extra)
	if startxref == "" {
		startxref = fmt.Sprint(xr)
	}
	fmt.Fprintf(&b, "%s\n%%%%EOF\n", startxref)
	return b.Bytes()
}

// streamFile has a cross-reference stream (uncompressed, W [1 2 1]); extra goes in its dictionary.
func streamFile(extra, startxref string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	o1 := b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	o2 := b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")
	o3 := b.Len()
	row := func(t byte, off int) []byte { return []byte{t, byte(off >> 8), byte(off), 0} }
	var data []byte
	data = append(data, row(0, 0)...)
	data = append(data, row(1, o1)...)
	data = append(data, row(1, o2)...)
	data = append(data, row(1, o3)...)
	fmt.Fprintf(&b, "3 0 obj\n<< /Type /XRef /Size 4 /W [1 2 1] /Root 1 0 R %s /Length %d >>\nstream\n", extra, len(data))
	b.Write(data)
	b.WriteString("\nendstream\nendobj\nstartxref\n")
	if startxref == "" {
		startxref = fmt.Sprint(o3)
	}
	fmt.Fprintf(&b, "%s\n%%%%EOF\n", startxref)
	return b.Bytes()
}

// mustNotPanic opens the file by both entrances and fails, with the panic, if the reader panics (the importer's
// recover would hide it in production; the reader itself must not need it).
func mustNotPanic(t *testing.T, name string, data []byte) *File {
	t.Helper()
	var f *File
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: openFile panicked: %v", name, r)
			}
		}()
		f, _ = openFile(context.Background(), data, Limits{}.fill())
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: Open panicked: %v", name, r)
			}
		}()
		_, _ = Open(context.Background(), data, Limits{})
	}()
	return f
}

func TestHugeXrefNumbersDoNotPanic(t *testing.T) {
	for _, c := range []struct{ name, extra, start string }{
		{"XRefStm", "/XRefStm " + huge, ""},
		{"XRefStm negative", "/XRefStm -" + huge, ""},
		{"Prev", "/Prev " + huge, ""},
		{"Prev negative", "/Prev -" + huge, ""},
		{"startxref", "", huge},
		{"XRefStm and Prev", "/XRefStm " + huge + " /Prev " + huge, ""},
	} {
		for _, kind := range []struct {
			name  string
			build func(extra, start string) []byte
		}{{"classic", classicFile}, {"xref stream", streamFile}} {
			mustNotPanic(t, kind.name+" "+c.name, kind.build(c.extra, c.start))
		}
	}
}

// The control: the same two files with no hostile number are read, so the test above is not passing on files that
// were never readable.
func TestHugeXrefControlFilesAreRead(t *testing.T) {
	for name, data := range map[string][]byte{"classic": classicFile("", ""), "xref stream": streamFile("", "")} {
		f := mustNotPanic(t, name, data)
		if f == nil || len(f.xref) < 2 {
			t.Errorf("%s: the control file's table was not read", name)
		}
	}
}

// An out-of-range /Prev or /XRefStm names nothing, and the table that was read is still used.
func TestHugeXrefStmIsIgnoredNotFatal(t *testing.T) {
	f := mustNotPanic(t, "classic", classicFile("/XRefStm "+huge, ""))
	if f == nil || len(f.xref) < 2 {
		t.Fatal("a file with an impossible /XRefStm lost its own table")
	}
}
