package check

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"math"
	"strconv"
)

// This file reads the subset of PDF syntax that publish/pdfw writes, as an
// independent reader: it shares no code with the writer, so a mistake in one
// is not repeated in the other. It is strict (an object must be exactly where
// the cross-reference table says, a stream exactly as long as /Length says)
// because a viewer that is lenient about such things is not the only reader.

type (
	pdfName string
	pdfNum  float64
	pdfRef  int
	pdfDict map[string]any
	pdfArr  []any
	pdfNull struct{}
	pdfBool bool
	pdfStr  struct {
		b   []byte
		hex bool
	}
	pdfStream struct {
		d    pdfDict
		data []byte // as stored (before any filter)
	}
	pdfOp string // an operator in a content stream
)

type pdfError struct{ msg string }

func (e pdfError) Error() string { return e.msg }

type lexer struct {
	b   []byte
	pos int
}

func (l *lexer) fail(format string, a ...any) {
	panic(pdfError{fmt.Sprintf(format, a...) + fmt.Sprintf(" (at byte %d)", l.pos)})
}

func isSpace(c byte) bool {
	return c == 0 || c == 9 || c == 10 || c == 12 || c == 13 || c == 32
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (l *lexer) skip() {
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		switch {
		case isSpace(c):
			l.pos++
		case c == '%':
			for l.pos < len(l.b) && l.b[l.pos] != '\n' && l.b[l.pos] != '\r' {
				l.pos++
			}
		default:
			return
		}
	}
}

func (l *lexer) atEnd() bool { l.skip(); return l.pos >= len(l.b) }

func (l *lexer) word() string {
	start := l.pos
	for l.pos < len(l.b) && !isSpace(l.b[l.pos]) && !isDelim(l.b[l.pos]) {
		l.pos++
	}
	return string(l.b[start:l.pos])
}

// value reads one value. In content mode a bare word is an operator; in
// object mode it is only true, false, null or a number or a reference.
func (l *lexer) value(content bool, depth int) any {
	if depth > 64 {
		l.fail("nesting is too deep")
	}
	l.skip()
	if l.pos >= len(l.b) {
		l.fail("unexpected end")
	}
	c := l.b[l.pos]
	switch {
	case c == '/':
		l.pos++
		w := l.word()
		// #xx escapes are not written by this program: refuse them.
		for i := 0; i < len(w); i++ {
			if w[i] == '#' {
				l.fail("a name with # escapes")
			}
		}
		return pdfName(w)
	case c == '<' && l.pos+1 < len(l.b) && l.b[l.pos+1] == '<':
		l.pos += 2
		d := pdfDict{}
		for {
			l.skip()
			if l.pos+1 < len(l.b) && l.b[l.pos] == '>' && l.b[l.pos+1] == '>' {
				l.pos += 2
				return d
			}
			k, ok := l.value(false, depth+1).(pdfName)
			if !ok {
				l.fail("a dictionary key is not a name")
			}
			if _, dup := d[string(k)]; dup {
				l.fail("duplicate key /%s", k)
			}
			d[string(k)] = l.value(false, depth+1)
		}
	case c == '<':
		l.pos++
		var hex []byte
		for {
			if l.pos >= len(l.b) {
				l.fail("a hex string is not closed")
			}
			ch := l.b[l.pos]
			l.pos++
			if ch == '>' {
				break
			}
			if isSpace(ch) {
				continue
			}
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
				l.fail("a hex string holds %q", ch)
			}
			hex = append(hex, ch)
		}
		if len(hex)%2 == 1 {
			hex = append(hex, '0')
		}
		out := make([]byte, len(hex)/2)
		for i := range out {
			v, _ := strconv.ParseUint(string(hex[2*i:2*i+2]), 16, 8)
			out[i] = byte(v)
		}
		return pdfStr{b: out, hex: true}
	case c == '(':
		l.pos++
		var out []byte
		nest := 1
		for {
			if l.pos >= len(l.b) {
				l.fail("a string is not closed")
			}
			ch := l.b[l.pos]
			l.pos++
			switch {
			case ch == '\\':
				if l.pos >= len(l.b) {
					l.fail("a string ends in a backslash")
				}
				e := l.b[l.pos]
				l.pos++
				switch {
				case e >= '0' && e <= '7':
					v := int(e - '0')
					for k := 0; k < 2 && l.pos < len(l.b) && l.b[l.pos] >= '0' && l.b[l.pos] <= '7'; k++ {
						v = v*8 + int(l.b[l.pos]-'0')
						l.pos++
					}
					out = append(out, byte(v))
				case e == 'n':
					out = append(out, '\n')
				case e == 'r':
					out = append(out, '\r')
				case e == 't':
					out = append(out, '\t')
				case e == 'b':
					out = append(out, 8)
				case e == 'f':
					out = append(out, 12)
				case e == '\n' || e == '\r':
				default:
					out = append(out, e)
				}
			case ch == '(':
				nest++
				out = append(out, ch)
			case ch == ')':
				nest--
				if nest == 0 {
					return pdfStr{b: out}
				}
				out = append(out, ch)
			default:
				out = append(out, ch)
			}
		}
	case c == '[':
		l.pos++
		a := pdfArr{}
		for {
			l.skip()
			if l.pos >= len(l.b) {
				l.fail("an array is not closed")
			}
			if l.b[l.pos] == ']' {
				l.pos++
				return a
			}
			a = append(a, l.value(content, depth+1))
		}
	case c == ']' || c == '>' || c == ')' || c == '{' || c == '}':
		l.fail("unexpected %q", c)
	}
	w := l.word()
	if w == "" {
		l.fail("unexpected %q", c)
	}
	switch w {
	case "true":
		return pdfBool(true)
	case "false":
		return pdfBool(false)
	case "null":
		return pdfNull{}
	}
	if isNumberToken(w) {
		f, err := strconv.ParseFloat(w, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			l.fail("number %q", w)
		}
		if !content && isUint(w) {
			// n g R
			save := l.pos
			l.skip()
			g := l.word()
			if isUint(g) {
				l.skip()
				if l.pos < len(l.b) && l.b[l.pos] == 'R' && (l.pos+1 >= len(l.b) || isSpace(l.b[l.pos+1]) || isDelim(l.b[l.pos+1])) {
					l.pos++
					if g != "0" {
						l.fail("a reference to generation %s", g)
					}
					n, _ := strconv.Atoi(w)
					return pdfRef(n)
				}
			}
			l.pos = save
		}
		return pdfNum(f)
	}
	if !content {
		l.fail("unexpected word %q", w)
	}
	return pdfOp(w)
}

func isUint(s string) bool {
	if s == "" || len(s) > 10 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isNumberToken(s string) bool {
	if s == "" {
		return false
	}
	digits, dots := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			dots++
		case (c == '-' || c == '+') && i == 0:
		default:
			return false
		}
	}
	return digits > 0 && dots <= 1
}

const maxInflate = 256 << 20

// inflate decodes a zlib stream and refuses one that decodes to more than max.
func inflate(data []byte, max int64) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > max {
		return nil, fmt.Errorf("the stream inflates to more than %d bytes", max)
	}
	return out, nil
}
