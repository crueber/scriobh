package pdfread

import "math"

// Numbers in a PDF are float64, and a hostile file can write any of them. Go's conversion of a float64 outside the
// range of an integer type is implementation-defined (amd64, arm64, 386 and arm each give a different answer), and
// an int is 32 bits on linux/arm, linux/386 and windows/386. These helpers make every conversion saturate, so a
// number means the same on every platform it fits on, and the one it does not fit on gives the same answer
// everywhere. They take the bounds of int as arguments where a test needs to ask what a 32-bit int would do.

// f64i converts a number from a file to an int64: NaN is 0 and a value out of range saturates. For every in-range
// value it is exactly int64(v), so it replaces int(v) and int64(v) without changing any in-range result. Values at or
// beyond 2^63 and NaN are the ones whose plain conversion is implementation-defined (amd64 gives MinInt64, arm64
// saturates, and a MinInt64 from a hostile offset passes a "< len" test); they now saturate the same way, to
// MaxInt64 and MinInt64, and NaN is 0, on every platform. Use it for every float64 to integer conversion of a
// number that came from a file.
func f64i(v float64) int64 {
	switch {
	case v != v:
		return 0
	case v >= 1<<63: // 1<<63 is exactly representable; MaxInt64 itself is not
		return math.MaxInt64
	case v <= -(1 << 63):
		return math.MinInt64
	}
	return int64(v)
}

// clampTo limits n to [lo, hi].
func clampTo(n, lo, hi int64) int64 { return min(max(n, lo), hi) }

// fnumBounds is fnum for an int whose range is [lo, hi].
func fnumBounds(v float64, lo, hi int64) int64 { return clampTo(f64i(v), lo, hi) }

// fnum converts a number from a file to an int, saturating at the ends of int (which are 32-bit on a 32-bit
// platform). Use it where the value is a count, an index or an object number and a platform's own answer for an
// out-of-range value must not decide what the file means.
func fnum(v float64) int { return int(fnumBounds(v, math.MinInt, math.MaxInt)) }

// fitsBounds says n is in [lo, hi], which is the range of an int for the caller's platform.
func fitsBounds(n, lo, hi int64) bool { return n >= lo && n <= hi }

// toInt narrows an int64 (an offset or a number from a cross-reference stream) to int when it fits and says so;
// a value that does not fit is not an object stream's number here, and must not wrap to one that is (2^32+5 would
// become 5 on a 32-bit int).
func toInt(n int64) (int, bool) {
	if !fitsBounds(n, math.MinInt, math.MaxInt) {
		return 0, false
	}
	return int(n), true
}

// satMul multiplies two non-negative numbers, saturating at MaxInt64.
func satMul(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

// predictorGeometry works out the byte width of one row of a predicted stream, and of one pixel, from the
// parameters in the file, in int64 and saturating, so that the product of three numbers a file chose cannot wrap
// (on a 32-bit int, colors*bpc*columns wraps to a small row from quite modest numbers). ok is false when the row is
// longer than maxRow: such a stream is returned as it is, as before.
//
// One result differs from the old code, and on purpose: a product that wrapped int (on a 32-bit int from modest
// numbers, on a 64-bit int only from absurd ones) used to give a small or negative row and the stream was decoded
// with the wrong geometry; it is now seen for the size it is, found over maxRow, and refused. Every product that did
// not wrap gives the row it gave. The pixel width is limited to the row, since a pixel wider than the row predicts
// nothing (as before).
func predictorGeometry(colors, bpc, columns, maxRow int64) (row, bpp int64, ok bool) {
	colors, bpc, columns = max(colors, 1), max(bpc, 1), max(columns, 1)
	bits := satMul(satMul(colors, bpc), columns)
	if bits > math.MaxInt64-7 {
		return 0, 0, false
	}
	row = (bits + 7) / 8
	if row <= 0 || row > maxRow {
		return 0, 0, false
	}
	bpp = max(1, satMul(colors, bpc)/8)
	return row, min(bpp, row), true
}

// objStmAt is the position in an object stream's data of the object at offset off from its /First, and false when
// that is not inside data (the sum is not formed when it could wrap).
func objStmAt(first, off, n int) (int, bool) {
	if first < 0 || off < 0 || first > n || off >= n-first {
		return 0, false
	}
	return first + off, true
}
