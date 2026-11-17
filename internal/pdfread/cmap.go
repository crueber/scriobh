package pdfread

import (
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

// CMaps: the ToUnicode map that says what a code means, and the encoding CMap
// of a composite font that says how many bytes make a code.

type codespace struct {
	lo, hi uint32
	n      int
}

type cidRange struct{ lo, hi, cid uint32 }

type cmap struct {
	spaces    []codespace
	uni       map[uint32]string
	cids      []cidRange
	wmode     int
	truncated bool
	sorted    bool
	cost      int64 // bytes charged for the entries
}

// parseCMap reads the part of a CMap program that matters: code space ranges,
// character and range mappings. Nothing in it is run.
func parseCMap(b []byte, maxEntries, depth int, bytesLeft int64, stop func() bool) *cmap {
	cm := &cmap{uni: map[uint32]string{}}
	// Every entry is charged the length of its text and 32 more, against bytesLeft, and the clock is asked every
	// few thousand entries; a destination longer than 16 characters is not a character's meaning and is not kept.
	charge := func(n int) bool {
		cm.cost += int64(n) + 32
		if cm.cost > bytesLeft {
			cm.truncated = true
			return false
		}
		return true
	}
	lx := newLexer(b, depth)
	var operands []any
	entries := 0
	code := func(v any) (uint32, int, bool) {
		s, ok := v.(String)
		if !ok || len(s) == 0 || len(s) > 4 {
			return 0, 0, false
		}
		var c uint32
		for _, x := range s {
			c = c<<8 | uint32(x)
		}
		return c, len(s), true
	}
	ustr := func(v any) string {
		s, ok := v.(String)
		if !ok {
			return ""
		}
		if len(s) == 1 {
			return string(rune(s[0]))
		}
		if len(s) > 64 { // more than a character's meaning: not kept
			return ""
		}
		if len(s)%2 == 1 {
			s = s[:len(s)-1]
		}
		u := make([]uint16, 0, len(s)/2)
		for i := 0; i+1 < len(s); i += 2 {
			u = append(u, uint16(s[i])<<8|uint16(s[i+1]))
		}
		return string(utf16.Decode(u))
	}
	for steps := 0; steps < 4_000_000; steps++ {
		if steps&0x3FFF == 0 && stop != nil && stop() {
			cm.truncated = true
			break
		}
		v, err := lx.next(false)
		if err != nil {
			break
		}
		op, isOp := v.(Op)
		if !isOp {
			operands = append(operands, v)
			if len(operands) > 4096 {
				operands = operands[len(operands)-4:]
			}
			continue
		}
		switch op {
		case "endcodespacerange":
			for i := 0; i+1 < len(operands); i += 2 {
				lo, n1, ok1 := code(operands[i])
				hi, n2, ok2 := code(operands[i+1])
				if ok1 && ok2 && n1 == n2 && len(cm.spaces) < 64 && charge(0) {
					cm.spaces = append(cm.spaces, codespace{lo, hi, n1})
				}
			}
		case "endbfchar":
			for i := 0; i+1 < len(operands); i += 2 {
				c, _, ok := code(operands[i])
				if !ok {
					continue
				}
				if entries++; entries > maxEntries || entries&0xFFF == 0 && stop != nil && stop() {
					cm.truncated = true
					break
				}
				s := ustr(operands[i+1])
				if s == "" || !charge(len(s)) {
					continue
				}
				cm.uni[c] = s
			}
		case "endbfrange":
			for i := 0; i+2 < len(operands); i += 3 {
				lo, _, ok1 := code(operands[i])
				hi, _, ok2 := code(operands[i+1])
				if !ok1 || !ok2 || hi < lo || hi-lo > 0xFFFF {
					continue
				}
				switch d := operands[i+2].(type) {
				case String:
					base := ustr(d)
					if base == "" {
						continue
					}
					rs := []rune(base)
					for c := lo; c <= hi; c++ {
						if entries++; entries > maxEntries || entries&0xFFF == 0 && stop != nil && stop() {
							cm.truncated = true
							break
						}
						r := append([]rune(nil), rs...)
						r[len(r)-1] += rune(c - lo)
						if !utf8.ValidRune(r[len(r)-1]) {
							continue
						}
						s := string(r)
						if !charge(len(s)) {
							break
						}
						cm.uni[c] = s
					}
				case Array:
					for k, e := range d {
						c := lo + uint32(k)
						if c > hi {
							break
						}
						if entries++; entries > maxEntries || entries&0xFFF == 0 && stop != nil && stop() {
							cm.truncated = true
							break
						}
						s := ustr(e)
						if s == "" || !charge(len(s)) {
							continue
						}
						cm.uni[c] = s
					}
				}
			}
		case "endcidrange":
			for i := 0; i+2 < len(operands); i += 3 {
				lo, _, ok1 := code(operands[i])
				hi, _, ok2 := code(operands[i+1])
				cid, ok3 := operands[i+2].(float64)
				if ok1 && ok2 && ok3 && hi >= lo && len(cm.cids) < maxEntries && charge(0) {
					cm.cids = append(cm.cids, cidRange{lo, hi, uint32(fint64(cid))})
				}
			}
		case "endcidchar":
			for i := 0; i+1 < len(operands); i += 2 {
				c, _, ok1 := code(operands[i])
				cid, ok2 := operands[i+1].(float64)
				if ok1 && ok2 && len(cm.cids) < maxEntries && charge(0) {
					cm.cids = append(cm.cids, cidRange{c, c, uint32(fint64(cid))})
				}
			}
		case "def":
			if len(operands) >= 2 {
				if n, ok := operands[len(operands)-2].(Name); ok && n == "WMode" {
					if w, ok := operands[len(operands)-1].(float64); ok {
						cm.wmode = fnum(w)
					}
				}
			}
		}
		if cm.truncated {
			break
		}
		operands = operands[:0]
	}
	return cm
}

// next returns the code at the start of s and its length in bytes.
func (cm *cmap) next(s []byte, def int) (uint32, int) {
	if len(s) == 0 {
		return 0, 0
	}
	if cm != nil && len(cm.spaces) > 0 {
		for n := 1; n <= 4 && n <= len(s); n++ {
			var c uint32
			for _, x := range s[:n] {
				c = c<<8 | uint32(x)
			}
			for _, sp := range cm.spaces {
				if sp.n == n && c >= sp.lo && c <= sp.hi {
					return c, n
				}
			}
		}
		n := 4
		for _, sp := range cm.spaces {
			n = min(n, sp.n)
		}
		def = n
	}
	n := min(def, len(s))
	var c uint32
	for _, x := range s[:n] {
		c = c<<8 | uint32(x)
	}
	return c, n
}

func (cm *cmap) cid(code uint32) uint32 {
	if !cm.sorted {
		sort.SliceStable(cm.cids, func(i, j int) bool { return cm.cids[i].lo < cm.cids[j].lo })
		cm.sorted = true
	}
	i := sort.Search(len(cm.cids), func(i int) bool { return cm.cids[i].lo > code })
	for k := i - 1; k >= 0 && k >= i-8; k-- {
		if r := cm.cids[k]; code <= r.hi {
			return r.cid + code - r.lo
		}
	}
	return code
}
