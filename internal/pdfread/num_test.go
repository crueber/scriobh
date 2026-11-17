package pdfread

import (
	"math"
	"testing"
)

// The bounds of a 32-bit int, so the tests ask on this machine what linux/arm, linux/386 and windows/386 would do.
const (
	min32 = math.MinInt32
	max32 = math.MaxInt32
)

func TestF64iSaturates(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want int64
	}{
		{0, 0}, {42.9, 42}, {-42.9, -42}, {1 << 40, 1 << 40}, {1 << 62, 1 << 62},
		{math.NaN(), 0}, {math.Inf(1), math.MaxInt64}, {math.Inf(-1), math.MinInt64},
		{1e30, math.MaxInt64}, {-1e30, math.MinInt64}, {1 << 63, math.MaxInt64}, {-(1 << 63), math.MinInt64},
	} {
		if got := f64i(c.in); got != c.want {
			t.Errorf("f64i(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFnumBoundsOf32BitInt(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want int64
	}{
		{5, 5}, {-5, -5}, {max32, max32}, {min32, min32},
		{max32 + 1, max32}, {1 << 32, max32}, {1<<32 + 5, max32}, {1e10, max32}, {-1e10, min32}, {math.Inf(1), max32}, {math.NaN(), 0},
	} {
		if got := fnumBounds(c.in, min32, max32); got != c.want {
			t.Errorf("a 32-bit int: fnum(%v) = %d, want %d", c.in, got, c.want)
		}
	}
	// On this platform the real fnum agrees with int for every in-range value, and saturates the rest.
	if fnum(123456) != 123456 || fnum(-7) != -7 || fnum(math.NaN()) != 0 {
		t.Error("fnum changed an in-range value")
	}
	if fnum(math.Inf(1)) != math.MaxInt || fnum(math.Inf(-1)) != math.MinInt {
		t.Error("fnum does not saturate")
	}
}

func TestFintKeepsItsClamp(t *testing.T) {
	if fint64(1e30) != 1<<40 || fint64(-1e30) != -(1<<40) || fint64(math.NaN()) != 0 || fint64(77) != 77 {
		t.Error("fint64 changed")
	}
	// An int that holds 2^40 gets it; a 32-bit int gets the end of its range, and nothing wraps.
	var limit int64 = 1 << 40
	if int64(fint(1e30)) != min(limit, math.MaxInt) || int64(fint(-1e30)) != max(-limit, math.MinInt) {
		t.Error("fint is not the 2^40 clamp (cut to int)")
	}
	// The value the uint32 sites take is the same on every platform: the low 32 bits of the clamped number.
	big := 5e9
	if got := uint32(fint64(big)); got != uint32(int64(big)) {
		t.Errorf("uint32(fint64(5e9)) = %d", got)
	}
}

func TestToIntBounds(t *testing.T) {
	// 2^32+5 must not be taken for object stream 5 where an int is 32 bits.
	for _, c := range []struct {
		n    int64
		fits bool
	}{
		{0, true}, {5, true}, {max32, true}, {min32, true}, {max32 + 1, false}, {1<<32 + 5, false}, {min32 - 1, false}, {math.MaxInt64, false},
	} {
		if got := fitsBounds(c.n, min32, max32); got != c.fits {
			t.Errorf("fits32(%d) = %v, want %v", c.n, got, c.fits)
		}
	}
	if n, ok := toInt(123); !ok || n != 123 {
		t.Error("toInt(123)")
	}
	if _, ok := toInt(math.MaxInt64); ok != (math.MaxInt == math.MaxInt64) {
		t.Error("toInt(MaxInt64) fits only where int is 64 bits")
	}
}

func TestObjStmAtDoesNotWrap(t *testing.T) {
	for _, c := range []struct {
		first, off, n int
		at            int
		ok            bool
	}{
		{10, 5, 100, 15, true}, {0, 0, 1, 0, true}, {10, 90, 100, 0, false}, {10, 89, 100, 99, true},
		{100, 0, 100, 0, false}, {101, 0, 100, 0, false}, {-1, 0, 100, 0, false}, {10, -1, 100, 0, false},
		// the sums that wrapped a 32-bit int to a negative number, or to a small positive one
		{max32, max32, 100, 0, false}, {1 << 30, 1 << 30, 100, 0, false}, {50, max32, 100, 0, false},
	} {
		at, ok := objStmAt(c.first, c.off, c.n)
		if ok != c.ok || ok && at != c.at {
			t.Errorf("objStmAt(%d, %d, %d) = %d, %v", c.first, c.off, c.n, at, ok)
		}
	}
}

func TestPredictorGeometry(t *testing.T) {
	const maxRow = 1 << 24
	for _, c := range []struct {
		colors, bpc, columns int64
		row, bpp             int64
		ok                   bool
	}{
		{1, 8, 1, 1, 1, true},
		{3, 8, 100, 300, 3, true},
		{4, 16, 50, 400, 8, true},
		{1, 1, 10, 2, 1, true}, // 10 bits is 2 bytes; a pixel is at least a byte
		{0, 0, 0, 1, 1, true},  // below 1 is read as 1
		{1, 8, maxRow, maxRow, 1, true},
		{1, 8, maxRow + 1, 0, 0, false},
		// a product that wrapped a 32-bit int to a small row, and one that wraps an int64
		{1 << 16, 8, 1 << 16, 0, 0, false},
		{1 << 11, 8, 1 << 20, 0, 0, false},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64, 0, 0, false},
		{math.MaxInt64, 1, 1, 0, 0, false},
		// a pixel wider than the row predicts nothing; it is limited to the row, not left to run past it
		{1000, 8, 1, 1000, 1000, true},
		{1000, 16, 1, 2000, 2000, true},
	} {
		row, bpp, ok := predictorGeometry(c.colors, c.bpc, c.columns, maxRow)
		if ok != c.ok || ok && (row != c.row || bpp != c.bpp) {
			t.Errorf("predictorGeometry(%d, %d, %d) = %d, %d, %v; want %d, %d, %v", c.colors, c.bpc, c.columns, row, bpp, ok, c.row, c.bpp, c.ok)
		}
		if ok && (bpp > row || bpp < 1) {
			t.Errorf("predictorGeometry(%d, %d, %d): bpp %d outside 1..row %d", c.colors, c.bpc, c.columns, bpp, row)
		}
	}
}

func TestPredictorHostileParametersAreNotApplied(t *testing.T) {
	in := []byte{2, 1, 2, 3, 2, 1, 2, 3}
	// colors*bpc*columns wraps a 32-bit int to a small number; the stream must come back untouched, not be cut by a wrong row.
	out := predictor(in, 15, 1<<16, 8, 1<<16)
	if string(out) != string(in) {
		t.Error("a predictor with an absurd row was applied")
	}
	if got := predictor(in, 1, 1, 8, 1); string(got) != string(in) {
		t.Error("predictor 1 is no predictor")
	}
}

func TestSatMul(t *testing.T) {
	if satMul(3, 4) != 12 || satMul(0, 5) != 0 || satMul(-1, 5) != 0 || satMul(math.MaxInt64, 2) != math.MaxInt64 || satMul(1<<32, 1<<32) != math.MaxInt64 {
		t.Error("satMul")
	}
}
