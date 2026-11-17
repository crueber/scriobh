// Package passkit is the passphrase machinery that a share link (internal/domain/share) and a protected project
// (internal/domain/projectgate) both use, so there is one way to hash a passphrase and one schedule for wrong tries:
//
//   - the hash is argon2id at the OWASP minimum cost, salted, stored as a PHC-style string, compared in constant time;
//   - the work is bounded: a Pool says "busy" instead of queueing, so a flood of attempts costs a bounded amount
//     of memory;
//   - wrong tries earn an escalating wait: nothing for the first three, then 30 seconds, doubling, at most an hour.
//
// It has no database and no knowledge of what is being protected: counting the tries, and where, is the caller's.
package passkit

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Lengths of a passphrase, in characters.
const (
	MinPassphrase = 8
	MaxPassphrase = 200
)

const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

// Hash returns the stored form of a passphrase. It takes no slot: callers bound the work with a Pool.
func Hash(p string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(p), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return "$argon2id$v=19$m=" + strconv.Itoa(argonMemory) + ",t=" + strconv.Itoa(argonTime) + ",p=" + strconv.Itoa(argonThreads) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(h), nil
}

// Verify reports whether p matches a hash made by Hash, in constant time. The cost parameters are read from the
// stored string but capped, so a hostile row cannot make a check expensive.
func Verify(encoded, p string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t, th int
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, _ := strings.Cut(kv, "=")
		n, err := strconv.Atoi(v)
		if err != nil {
			return false
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			th = n
		}
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || m <= 0 || t <= 0 || th <= 0 || m > 1<<20 || t > 10 || th > 8 || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, uint32(t), uint32(m), uint8(th), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// The schedule of waits after wrong tries in a row.
const (
	FreeTries   = 3
	FirstDelay  = 30 * time.Second
	MaxDelay    = time.Hour
	FailureLife = 24 * time.Hour // a counter that has been quiet this long starts again
)

// Delay is the wait after the nth wrong try in a row.
func Delay(failed int) time.Duration {
	if failed <= FreeTries {
		return 0
	}
	d := FirstDelay
	for i := FreeTries + 1; i < failed && d < MaxDelay; i++ {
		d *= 2
	}
	if d > MaxDelay {
		d = MaxDelay
	}
	return d
}

// Pool bounds how many hashes run at once.
type Pool struct{ slots chan struct{} }

// NewPool makes a pool of n slots.
func NewPool(n int) *Pool { return &Pool{slots: make(chan struct{}, n)} }

// TryAcquire takes a slot without waiting. ok is false when all are taken: answer "busy", do not queue.
func (p *Pool) TryAcquire() (release func(), ok bool) {
	select {
	case p.slots <- struct{}{}:
		return func() { <-p.slots }, true
	default:
		return nil, false
	}
}

// Acquire waits for a slot. It is for the owner's own work (setting a passphrase), which is not an attack.
func (p *Pool) Acquire() (release func()) {
	p.slots <- struct{}{}
	return func() { <-p.slots }
}
