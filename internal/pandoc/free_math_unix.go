//go:build unix

package pandoc

import "math"

// statCount is the type of a block count or a block size in any Unix's statfs: Linux and macOS use unsigned counts
// (and a signed or 32-bit size), FreeBSD signed 64-bit ones, OpenBSD a signed count and a 32-bit size.
type statCount interface {
	~int32 | ~uint32 | ~int64 | ~uint64
}

// freeFrom is the bytes an unprivileged user may still use: the blocks available to them times the size of a block.
// A negative count (FreeBSD's Bavail goes below zero when a volume is over its reserve) means no room, and is
// read as zero; reading it as unsigned would make a full disk look enormous and let the check pass. A product
// that does not fit is as large as can be said.
func freeFrom[A, B statCount](avail A, size B) uint64 {
	if avail < 0 || size < 0 {
		return 0
	}
	a, s := uint64(avail), uint64(size)
	if s != 0 && a > math.MaxUint64/s {
		return math.MaxUint64
	}
	return a * s
}
