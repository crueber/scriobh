package pdfread

import (
	"errors"
	"math"
	"strconv"
)

// The PDF object model, as small Go types.
type (
	Name   string
	Ref    struct{ Num, Gen int }
	Dict   map[string]any
	Array  []any
	String []byte
	Op     string // an operator (a keyword) in a content stream or at the top of an object
	Stream struct {
		Dict Dict
		raw  []byte // as stored, before any filter (a slice of the file)
	}
)

var errSyntax = errors.New("pdf syntax")

func isWS(c byte) bool { return c == 0 || c == 9 || c == 10 || c == 12 || c == 13 || c == 32 }
func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}
func isReg(c byte) bool { return !isWS(c) && !isDelim(c) }

// lexer reads objects and operators from bytes. It never panics and never
// recurses deeper than depth.
type lexer struct {
	b      []byte
	pos    int
	depth  int
	max    int
	budget int // tokens left; 0 means exhausted
	// An estimate of the memory the parsed value takes (a boxed number, a dictionary's map ...), charged as it is
	// parsed: past maxMem the parse stops. 0 means no limit.
	mem, maxMem int64
}

var errTooBig = errors.New("object too large")

func (l *lexer) charge(n int64) error {
	l.mem += n
	if l.maxMem > 0 && l.mem > l.maxMem {
		return errTooBig
	}
	return nil
}

func newLexer(b []byte, maxDepth int) *lexer { return &lexer{b: b, max: maxDepth, budget: math.MaxInt} }

func (l *lexer) skip() {
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		switch {
		case isWS(c):
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

func (l *lexer) eof() bool { l.skip(); return l.pos >= len(l.b) }

// next reads one object, or an Op for a keyword, or errSyntax. "n g R" is a Ref
// when withRefs is set.
func (l *lexer) next(withRefs bool) (any, error) {
	v, err := l.next0(withRefs)
	if l.maxMem == 0 || err != nil && !isContainer(v) {
		return v, err
	}
	// What the value takes: a boxed number or name about 48 bytes, a string its length more, an array 96 and 16 an
	// element, a dictionary 320 and 100 a key (measured: a parsed byte of text costs 20 to 50 bytes).
	var cost int64 = 48
	switch x := v.(type) {
	case String:
		cost += int64(len(x))
	case Name:
		cost += int64(len(x))
	case Array:
		cost = 96 + 16*int64(len(x))
	case Dict:
		cost = 320 + 100*int64(len(x))
	}
	if cerr := l.charge(cost); cerr != nil {
		return nil, cerr
	}
	return v, err
}

func isContainer(v any) bool {
	switch v.(type) {
	case Array, Dict:
		return true
	}
	return false
}

func (l *lexer) next0(withRefs bool) (any, error) {
	l.skip()
	if l.pos >= len(l.b) {
		return nil, errors.New("end of data")
	}
	l.budget--
	if l.budget < 0 {
		return nil, errors.New("too many tokens")
	}
	c := l.b[l.pos]
	switch {
	case c == '/':
		l.pos++
		return l.name(), nil
	case c == '(':
		return l.litString()
	case c == '<':
		if l.pos+1 < len(l.b) && l.b[l.pos+1] == '<' {
			return l.dict(withRefs)
		}
		return l.hexString()
	case c == '[':
		return l.array(withRefs)
	case c == ']' || c == '>' || c == ')' || c == '{' || c == '}':
		l.pos++
		if c == '>' && l.pos < len(l.b) && l.b[l.pos] == '>' {
			l.pos++
			return Op(">>"), nil
		}
		return Op(string(c)), nil
	case c == '+' || c == '-' || c == '.' || c >= '0' && c <= '9':
		start := l.pos
		for l.pos < len(l.b) && isReg(l.b[l.pos]) {
			l.pos++
		}
		tok := l.b[start:l.pos]
		f, ok := parseNumber(tok)
		if !ok {
			return Op(string(tok)), nil
		}
		if withRefs && isUint(tok) {
			if r, ok := l.tryRef(fnum(f)); ok {
				return r, nil
			}
		}
		return f, nil
	}
	start := l.pos
	for l.pos < len(l.b) && isReg(l.b[l.pos]) {
		l.pos++
	}
	if l.pos == start { // cannot happen: a delimiter was handled above
		l.pos++
		return nil, errSyntax
	}
	switch string(l.b[start:l.pos]) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	return Op(string(l.b[start:l.pos])), nil
}

func isUint(t []byte) bool {
	if len(t) == 0 || len(t) > 10 {
		return false
	}
	for _, c := range t {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseNumber(t []byte) (float64, bool) {
	if len(t) == 0 || len(t) > 40 {
		return 0, false
	}
	// A real or an integer, leniently: "--5" and "5-3" occur in the wild and mean what a viewer takes them to.
	i := 0
	neg := false
	for i < len(t) && (t[i] == '+' || t[i] == '-') {
		if t[i] == '-' {
			neg = true
		}
		i++
	}
	var v float64
	digits := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		v = v*10 + float64(t[i]-'0')
		i++
		digits++
	}
	if i < len(t) && t[i] == '.' {
		i++
		scale := 0.1
		for i < len(t) && t[i] >= '0' && t[i] <= '9' {
			v += float64(t[i]-'0') * scale
			scale /= 10
			i++
			digits++
		}
	}
	if digits == 0 {
		return 0, false
	}
	if i < len(t) { // "5-3": the rest is ignored; letters are no number
		for _, c := range t[i:] {
			if c != '-' && c != '.' && (c < '0' || c > '9') {
				return 0, false
			}
		}
	}
	if neg {
		v = -v
	}
	return v, true
}

// tryRef reads "g R" after an integer n, restoring the position when it is not one.
func (l *lexer) tryRef(n int) (Ref, bool) {
	save := l.pos
	l.skip()
	s := l.pos
	for l.pos < len(l.b) && l.b[l.pos] >= '0' && l.b[l.pos] <= '9' {
		l.pos++
	}
	if l.pos == s || l.pos-s > 5 {
		l.pos = save
		return Ref{}, false
	}
	g, _ := strconv.Atoi(string(l.b[s:l.pos]))
	l.skip()
	if l.pos < len(l.b) && l.b[l.pos] == 'R' && (l.pos+1 >= len(l.b) || !isReg(l.b[l.pos+1])) {
		l.pos++
		return Ref{n, g}, true
	}
	l.pos = save
	return Ref{}, false
}

func (l *lexer) name() Name {
	var out []byte
	for l.pos < len(l.b) && isReg(l.b[l.pos]) {
		c := l.b[l.pos]
		if c == '#' && l.pos+2 < len(l.b) && isHexDigit(l.b[l.pos+1]) && isHexDigit(l.b[l.pos+2]) {
			out = append(out, hexVal(l.b[l.pos+1])<<4|hexVal(l.b[l.pos+2]))
			l.pos += 3
			continue
		}
		out = append(out, c)
		l.pos++
		if len(out) > 4096 {
			break
		}
	}
	return Name(out)
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

func (l *lexer) litString() (String, error) {
	l.pos++ // (
	depth := 1
	var out []byte
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		l.pos++
		switch c {
		case '\\':
			if l.pos >= len(l.b) {
				return String(out), nil
			}
			e := l.b[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\r':
				if l.pos < len(l.b) && l.b[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for k := 0; k < 2 && l.pos < len(l.b) && l.b[l.pos] >= '0' && l.b[l.pos] <= '7'; k++ {
					v = v*8 + int(l.b[l.pos]-'0')
					l.pos++
				}
				out = append(out, byte(v))
			default:
				out = append(out, e)
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return String(out), nil
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return String(out), nil // unterminated: what there is
}

func (l *lexer) hexString() (String, error) {
	l.pos++ // <
	var out []byte
	hi := -1
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		l.pos++
		if c == '>' {
			break
		}
		if !isHexDigit(c) {
			continue
		}
		if hi < 0 {
			hi = int(hexVal(c))
		} else {
			out = append(out, byte(hi<<4)|hexVal(c))
			hi = -1
		}
	}
	if hi >= 0 {
		out = append(out, byte(hi<<4))
	}
	return String(out), nil
}

func (l *lexer) array(withRefs bool) (Array, error) {
	l.pos++ // [
	l.depth++
	defer func() { l.depth-- }()
	if l.depth > l.max {
		return nil, errors.New("nested too deeply")
	}
	var out Array
	for {
		v, err := l.next(withRefs)
		if err != nil {
			return out, err
		}
		if op, ok := v.(Op); ok {
			if op == "]" {
				return out, nil
			}
			if op == ">>" || op == "endobj" || op == "stream" {
				return out, errSyntax
			}
			continue // a stray keyword inside an array is skipped
		}
		out = append(out, v)
		if len(out) > 1_000_000 {
			return out, errors.New("array too long")
		}
	}
}

func (l *lexer) dict(withRefs bool) (Dict, error) {
	l.pos += 2 // <<
	l.depth++
	defer func() { l.depth-- }()
	if l.depth > l.max {
		return nil, errors.New("nested too deeply")
	}
	d := Dict{}
	for {
		k, err := l.next(withRefs)
		if err != nil {
			return d, err
		}
		if op, ok := k.(Op); ok {
			if op == ">>" {
				return d, nil
			}
			if op == "endobj" || op == "stream" {
				return d, errSyntax
			}
			continue
		}
		key, ok := k.(Name)
		if !ok {
			continue
		}
		v, err := l.next(withRefs)
		if err != nil {
			return d, err
		}
		if op, ok := v.(Op); ok {
			if op == ">>" {
				return d, nil
			}
			continue
		}
		if len(d) < 100_000 {
			d[string(key)] = v
		}
	}
}
