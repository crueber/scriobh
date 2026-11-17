//go:build unix

package pandoc

import "math"

// syscall.Rlimit's two fields are uint64 on Linux, macOS and OpenBSD and int64 on FreeBSD, and the field is the
// only thing that differs. These generic helpers take the field's own type, so one body serves every Unix and no
// platform's limit is skipped or set to a wrapped value.

// rlimField is the type of a syscall.Rlimit field on any Unix.
type rlimField interface{ ~int64 | ~uint64 }

// rlimValue reads a field as an unsigned number. A negative signed value (no Unix reports one for a limit) is read
// as no limit at all, which is the largest number, so that neverRaise then applies the limit asked for.
func rlimValue[T rlimField](x T) uint64 {
	if x < 0 {
		return math.MaxUint64
	}
	return uint64(x)
}

// setRlim stores v in a field. Where the field is signed, a v above its largest value is stored as that largest
// value (which is "unlimited" on FreeBSD, RLIM_INFINITY being MaxInt64 there), not wrapped to a negative number,
// which the kernel would refuse or read as a tiny limit.
func setRlim[T rlimField](p *T, v uint64) {
	var zero T
	if ^zero < zero && v > math.MaxInt64 { // ^0 is -1 only in a signed type
		v = math.MaxInt64
	}
	*p = T(v)
}

// neverRaise returns the limit to set: the one asked for, or the current hard limit when that is lower, because the
// helper only ever lowers what Pandoc may use.
func neverRaise(asked, hard uint64) uint64 { return min(asked, hard) }
