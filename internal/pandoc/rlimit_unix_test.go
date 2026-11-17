//go:build unix

package pandoc

import (
	"math"
	"testing"
)

// The two field types syscall.Rlimit has on the Unixes: uint64 (Linux, macOS, OpenBSD) and int64 (FreeBSD). Both are
// exercised here on whatever machine the tests run on, so the FreeBSD path is tested without FreeBSD.
type (
	signedField   int64
	unsignedField uint64
)

func TestSetRlimSignedField(t *testing.T) {
	var f signedField
	setRlim(&f, 1<<20)
	if f != 1<<20 {
		t.Errorf("a small limit became %d", f)
	}
	setRlim(&f, math.MaxInt64)
	if f != math.MaxInt64 {
		t.Errorf("MaxInt64 became %d", f)
	}
	for _, v := range []uint64{math.MaxInt64 + 1, 1 << 63, math.MaxUint64} {
		setRlim(&f, v)
		if f != math.MaxInt64 || f < 0 {
			t.Errorf("%d stored as %d in a signed field; it must saturate, never wrap negative", v, f)
		}
	}
}

func TestSetRlimUnsignedField(t *testing.T) {
	var f unsignedField
	for _, v := range []uint64{0, 1, 1 << 40, math.MaxInt64 + 1, math.MaxUint64} {
		setRlim(&f, v)
		if uint64(f) != v {
			t.Errorf("%d stored as %d in an unsigned field", v, f)
		}
	}
}

func TestRlimValue(t *testing.T) {
	if rlimValue(signedField(77)) != 77 || rlimValue(unsignedField(77)) != 77 {
		t.Error("an ordinary limit changed")
	}
	if rlimValue(signedField(math.MaxInt64)) != math.MaxInt64 {
		t.Error("FreeBSD's unlimited changed")
	}
	if rlimValue(signedField(-1)) != math.MaxUint64 {
		t.Error("a negative limit is read as none, so the asked limit applies")
	}
}

func TestNeverRaise(t *testing.T) {
	if neverRaise(100, 50) != 50 {
		t.Error("a limit above the hard limit must be lowered to it")
	}
	if neverRaise(100, 500) != 100 {
		t.Error("a limit under the hard limit is the one asked for")
	}
	if neverRaise(100, rlimValue(unsignedField(math.MaxUint64))) != 100 || neverRaise(100, rlimValue(signedField(math.MaxInt64))) != 100 {
		t.Error("with no hard limit the asked limit applies, on either field type")
	}
}

func TestFreeFrom(t *testing.T) {
	cases := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"linux amd64 (uint64, int64)", freeFrom(uint64(1000), int64(4096)), 1000 * 4096},
		{"linux 32-bit (uint64, int32)", freeFrom(uint64(1000), int32(4096)), 1000 * 4096},
		{"macOS (uint64, uint32)", freeFrom(uint64(1000), uint32(4096)), 1000 * 4096},
		{"freebsd (int64, uint64)", freeFrom(int64(1000), uint64(4096)), 1000 * 4096},
		{"openbsd (int64, uint32)", freeFrom(int64(1000), uint32(4096)), 1000 * 4096},
		{"netbsd (uint64, uint64)", freeFrom(uint64(1000), uint64(4096)), 1000 * 4096},
		// FreeBSD's available blocks go negative when the volume is past its reserve: that is no room, not a vast one.
		{"negative available", freeFrom(int64(-5), uint64(4096)), 0},
		{"most negative available", freeFrom(int64(math.MinInt64), uint64(4096)), 0},
		{"zero blocks", freeFrom(uint64(0), int64(4096)), 0},
		{"zero size", freeFrom(uint64(1000), int64(0)), 0},
		{"negative size", freeFrom(uint64(1000), int64(-1)), 0},
		{"overflowing product", freeFrom(uint64(math.MaxUint64), uint64(4096)), math.MaxUint64},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestFreeBytesAnswersHere(t *testing.T) {
	free, ok := freeBytes(t.TempDir())
	if !ok || free == 0 {
		t.Errorf("freeBytes of a temp folder = %d, %v", free, ok)
	}
	if _, ok := freeBytes(t.TempDir() + "/does-not-exist"); ok {
		t.Error("a folder that is not there reported room")
	}
}
