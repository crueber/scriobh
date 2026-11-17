// Package pdftest assembles small PDF files by hand for tests: any object, any
// stream filter, a cross-reference table or stream, object streams, and the
// damage a hostile or careless producer does. It is used by tests only.
package pdftest

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"sort"
	"strings"
)

// Doc is a file being assembled.
type Doc struct {
	objs map[int][]byte
	next int
}

// New starts a document; object numbers begin at 1.
func New() *Doc { return &Doc{objs: map[int][]byte{}, next: 1} }

// Alloc reserves an object number.
func (d *Doc) Alloc() int { n := d.next; d.next++; return n }

// Set stores the text of object n (a dictionary, an array, a number ...).
func (d *Doc) Set(n int, body string) { d.objs[n] = []byte(body) }

// Add stores a new object.
func (d *Doc) Add(body string) int { n := d.Alloc(); d.Set(n, body); return n }

// Stream stores a stream object. filter is "", "Flate", "A85", "AHx", "RL" or
// "LZW", or a chain joined by "+" (applied left to right, written in the
// order a reader undoes them).
func (d *Doc) Stream(dict string, data []byte, filter string) int {
	n := d.Alloc()
	d.SetStream(n, dict, data, filter)
	return n
}

// SetStream stores a stream at an object number.
func (d *Doc) SetStream(n int, dict string, data []byte, filter string) {
	var names []string
	if filter != "" {
		parts := strings.Split(filter, "+")
		for _, f := range parts {
			switch f {
			case "Flate":
				data = deflate(data)
				names = append([]string{"/FlateDecode"}, names...)
			case "A85":
				data = ascii85([]byte(data))
				names = append([]string{"/ASCII85Decode"}, names...)
			case "AHx":
				data = []byte(fmt.Sprintf("%X>", data))
				names = append([]string{"/ASCIIHexDecode"}, names...)
			case "RL":
				data = runLength(data)
				names = append([]string{"/RunLengthDecode"}, names...)
			case "LZW":
				data = lzwEncode(data)
				names = append([]string{"/LZWDecode"}, names...)
			}
		}
	}
	var b bytes.Buffer
	b.WriteString("<< ")
	b.WriteString(dict)
	if len(names) == 1 {
		b.WriteString(" /Filter " + names[0])
	} else if len(names) > 1 {
		b.WriteString(" /Filter [" + strings.Join(names, " ") + "]")
	}
	fmt.Fprintf(&b, " /Length %d >>\nstream\n", len(data))
	b.Write(data)
	b.WriteString("\nendstream")
	d.objs[n] = b.Bytes()
}

// Raw stores an object exactly as given, whatever it holds.
func (d *Doc) Raw(n int, b []byte) { d.objs[n] = b }

func deflate(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func ascii85(b []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(b); i += 4 {
		n := min(4, len(b)-i)
		var v uint32
		for k := 0; k < 4; k++ {
			v <<= 8
			if k < n {
				v |= uint32(b[i+k])
			}
		}
		if n == 4 && v == 0 {
			out.WriteByte('z')
			continue
		}
		var g [5]byte
		for k := 4; k >= 0; k-- {
			g[k] = byte(v%85) + '!'
			v /= 85
		}
		out.Write(g[:n+1])
	}
	out.WriteString("~>")
	return out.Bytes()
}

func runLength(b []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(b); {
		j := i
		for j < len(b) && j-i < 128 && b[j] == b[i] {
			j++
		}
		if j-i >= 3 {
			out.WriteByte(byte(257 - (j - i)))
			out.WriteByte(b[i])
			i = j
			continue
		}
		k := i
		for k < len(b) && k-i < 128 && !(k+2 < len(b) && b[k] == b[k+1] && b[k] == b[k+2]) {
			k++
		}
		if k == i {
			k = i + 1
		}
		out.WriteByte(byte(k - i - 1))
		out.Write(b[i:k])
		i = k
	}
	out.WriteByte(128)
	return out.Bytes()
}

// lzwEncode writes PDF's LZW (MSB first, early change 1).
func lzwEncode(b []byte) []byte {
	var out bytes.Buffer
	var acc uint32
	nb := 0
	width := 9
	put := func(code int) {
		acc = acc<<uint(width) | uint32(code)
		nb += width
		for nb >= 8 {
			out.WriteByte(byte(acc >> uint(nb-8)))
			nb -= 8
		}
		acc &= 1<<uint(nb) - 1
	}
	table := map[string]int{}
	reset := func() {
		table = map[string]int{}
		for i := 0; i < 256; i++ {
			table[string([]byte{byte(i)})] = i
		}
	}
	reset()
	next := 258
	put(256)
	cur := ""
	for _, c := range b {
		cand := cur + string([]byte{c})
		if _, ok := table[cand]; ok {
			cur = cand
			continue
		}
		put(table[cur])
		table[cand] = next
		next++
		switch {
		case next+1 > 2048:
			width = 12
		case next+1 > 1024:
			width = 11
		case next+1 > 512:
			width = 10
		}
		if next >= 4094 {
			put(256)
			reset()
			next, width = 258, 9
		}
		cur = string([]byte{c})
	}
	if cur != "" {
		put(table[cur])
	}
	put(257)
	if nb > 0 {
		out.WriteByte(byte(acc << uint(8-nb)))
	}
	return out.Bytes()
}

// Opt says how the file is written.
type Opt struct {
	XrefStream bool   // a cross-reference stream instead of a table
	ObjStm     bool   // plain dictionaries go into one object stream (with XrefStream)
	NoXref     bool   // no table and no startxref: a reader must rebuild
	BadXref    bool   // a table whose offsets are wrong
	Info       int    // the Info dictionary's object, if any
	Trailer    string // extra trailer entries
	Prefix     string // bytes before %PDF (junk)
	Version    string
	Truncate   int // cut this many bytes off the end
}

// Write assembles the file with root as its catalog.
func (d *Doc) Write(root int, o Opt) []byte {
	var b bytes.Buffer
	b.WriteString(o.Prefix)
	v := o.Version
	if v == "" {
		v = "1.7"
	}
	b.WriteString("%PDF-" + v + "\n%\xe2\xe3\xcf\xd3\n")
	nums := make([]int, 0, len(d.objs))
	for n := range d.objs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	offs := map[int]int{}
	inStm := map[int]bool{}
	var stmNum int
	if o.ObjStm {
		stmNum = d.next
		d.next++
		var hdr, body bytes.Buffer
		cnt := 0
		for _, n := range nums {
			ob := d.objs[n]
			if bytes.Contains(ob, []byte("stream\n")) || n == root && false {
				continue
			}
			fmt.Fprintf(&hdr, "%d %d ", n, body.Len())
			body.Write(ob)
			body.WriteByte('\n')
			inStm[n] = true
			cnt++
		}
		first := hdr.Len()
		data := append(hdr.Bytes(), body.Bytes()...)
		d.SetStream(stmNum, fmt.Sprintf("/Type /ObjStm /N %d /First %d", cnt, first), data, "Flate")
		nums = append(nums, stmNum)
	}
	for _, n := range nums {
		if inStm[n] {
			continue
		}
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n", n)
		b.Write(d.objs[n])
		b.WriteString("\nendobj\n")
	}
	size := d.next
	trailer := fmt.Sprintf("/Size %d /Root %d 0 R", size, root)
	if o.Info != 0 {
		trailer += fmt.Sprintf(" /Info %d 0 R", o.Info)
	}
	trailer += " " + o.Trailer
	switch {
	case o.NoXref:
		fmt.Fprintf(&b, "trailer\n<< %s >>\n", trailer)
	case o.XrefStream:
		xr := size
		size++
		trailer = strings.Replace(trailer, fmt.Sprintf("/Size %d", d.next), fmt.Sprintf("/Size %d", size), 1)
		xoff := b.Len()
		var rows bytes.Buffer
		for n := 0; n < size; n++ {
			switch {
			case n == xr:
				rows.Write([]byte{1, byte(xoff >> 16), byte(xoff >> 8), byte(xoff), 0})
			case inStm[n]:
				idx := 0
				for _, m := range nums {
					if m == n {
						break
					}
					if inStm[m] {
						idx++
					}
				}
				rows.Write([]byte{2, byte(stmNum >> 16), byte(stmNum >> 8), byte(stmNum), byte(idx)})
			default:
				if off, ok := offs[n]; ok {
					rows.Write([]byte{1, byte(off >> 16), byte(off >> 8), byte(off), 0})
				} else {
					rows.Write([]byte{0, 0, 0, 0, 0})
				}
			}
		}
		data := deflate(rows.Bytes())
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /W [1 3 1] /Index [0 %d] %s /Filter /FlateDecode /Length %d >>\nstream\n", xr, size, trailer, len(data))
		b.Write(data)
		fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xoff)
	default:
		xoff := b.Len()
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", size)
		for n := 1; n < size; n++ {
			off, ok := offs[n]
			switch {
			case !ok:
				b.WriteString("0000000000 65535 f \n")
			case o.BadXref:
				fmt.Fprintf(&b, "%010d 00000 n \n", off+7)
			default:
				fmt.Fprintf(&b, "%010d 00000 n \n", off)
			}
		}
		fmt.Fprintf(&b, "trailer\n<< %s >>\nstartxref\n%d\n%%%%EOF\n", trailer, xoff)
	}
	out := b.Bytes()
	if o.Truncate > 0 && o.Truncate < len(out) {
		out = out[:len(out)-o.Truncate]
	}
	return out
}

// Pages makes the page tree and the catalog over page objects (each created
// with Page) and returns the catalog.
func (d *Doc) Pages(pages []int, catalogExtra string) int {
	pn := d.Alloc()
	var kids strings.Builder
	for _, p := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", p)
		d.objs[p] = bytes.Replace(d.objs[p], []byte("/Parent 0 0 R"), []byte(fmt.Sprintf("/Parent %d 0 R", pn)), 1)
	}
	d.Set(pn, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), len(pages)))
	return d.Add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R %s >>", pn, catalogExtra))
}

// Page makes a page of the given size with resources (the text between << >>)
// and a content stream.
func (d *Doc) Page(w, h int, resources string, content string, filter string) int {
	cs := d.Stream("", []byte(content), filter)
	return d.Add(fmt.Sprintf("<< /Type /Page /Parent 0 0 R /MediaBox [0 0 %d %d] /Resources << %s >> /Contents %d 0 R >>", w, h, resources, cs))
}

// Esc escapes a string for a literal string in a content stream.
func Esc(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)")
	return "(" + r.Replace(s) + ")"
}

// RawObj returns the text of object n as stored.
func (d *Doc) RawObj(n int) []byte { return d.objs[n] }
