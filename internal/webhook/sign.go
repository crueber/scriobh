package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"
)

// Headers of a delivery.
const (
	HeaderID        = "Scriobh-Webhook-Id"
	HeaderTimestamp = "Scriobh-Webhook-Timestamp"
	HeaderSignature = "Scriobh-Webhook-Signature"
	HeaderEvent     = "Scriobh-Webhook-Event"
	HeaderAttempt   = "Scriobh-Webhook-Attempt"
)

// SecretPrefix starts every signing secret, so a leaked one is recognisable.
const SecretPrefix = "whsec_"

// NewSecret returns a fresh signing secret: 32 random bytes, URL-safe.
func NewSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("webhook: entropy source failed: " + err.Error())
	}
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// Hint is the part of a secret that may be shown after it was made: enough to
// tell two apart, not enough to help anyone guess.
func Hint(secret string) string {
	if len(secret) < len(SecretPrefix)+4 {
		return SecretPrefix + "…"
	}
	return secret[:len(SecretPrefix)+4] + "…"
}

// Sign returns the signature of a delivery: the lowercase hex HMAC-SHA256, under
// the secret, of the timestamp (unix seconds, decimal), a full stop, and the
// body exactly as sent. The timestamp is inside the signed text so a captured
// delivery cannot be replayed later, and the receiver is told to check it.
func Sign(secret string, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte{'.'})
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// SignatureHeader is the header value for one or more secrets: "v1=<hex>" for
// each, comma separated. A second one is present while a rotated secret is
// still honoured, so a receiver can change over without a gap.
func SignatureHeader(timestamp int64, body []byte, secrets ...string) string {
	parts := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			parts = append(parts, "v1="+Sign(s, timestamp, body))
		}
	}
	return strings.Join(parts, ", ")
}

// Verify is what a receiver does, and what the tests and the documentation use:
// it accepts a delivery whose timestamp is within tolerance of now and whose
// header holds a v1 signature that matches the body under the secret. The
// comparison takes the same time whatever the input.
func Verify(secret string, header, timestampHeader string, body []byte, now time.Time, tolerance time.Duration) bool {
	ts, err := strconv.ParseInt(strings.TrimSpace(timestampHeader), 10, 64)
	if err != nil {
		return false
	}
	if math.Abs(float64(now.Unix()-ts)) > tolerance.Seconds() {
		return false
	}
	want := Sign(secret, ts, body)
	ok := false
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if sig, found := strings.CutPrefix(part, "v1="); found {
			if hmac.Equal([]byte(sig), []byte(want)) {
				ok = true
			}
		}
	}
	return ok
}
