package share

import (
	"strings"
	"testing"
	"time"
)

func TestTokensAreLongRandomAndOnlyTheirHashIsKept(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, token, hash := NewToken()
		m := tokenRe.FindStringSubmatch(token)
		if m == nil || m[1] != id {
			t.Fatalf("token %q", token)
		}
		secret := m[2]
		if len(secret) != 43 { // 256 bits in base64url
			t.Errorf("secret length %d", len(secret))
		}
		if hash != hashSecret(secret) || len(hash) != 64 || strings.Contains(hash, secret) {
			t.Errorf("hash %q", hash)
		}
		if seen[secret] || seen[id] {
			t.Fatal("a repeat")
		}
		seen[secret], seen[id] = true, true
	}
	for _, bad := range []string{"", "x", "shl_00000000000000000000000000", "shl_00000000000000000000000000.", "shl_0000000000000000000000000.abc", strings.Repeat("a", 500)} {
		if tokenRe.MatchString(bad) {
			t.Errorf("%q is a token", bad)
		}
	}
}

func TestPassphrasesAreHashedAndProofsAreBoundToTheLink(t *testing.T) {
	enc, err := HashPassphrase("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$") || strings.Contains(enc, "correct horse") {
		t.Fatalf("stored form %q", enc)
	}
	if !verifyPassphrase(enc, "correct horse battery") || verifyPassphrase(enc, "correct horse batterY") || verifyPassphrase(enc, "") {
		t.Error("verify")
	}
	for _, junk := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$x$y", "$argon2id$v=19$m=99999999,t=1,p=1$AAAA$AAAA", "$argon2id$v=19$m=a,t=1,p=1$AAAA$AAAA"} {
		if verifyPassphrase(junk, "x") {
			t.Errorf("%q verified", junk)
		}
	}
	key := UnlockKey([]byte("instance secret"))
	other := UnlockKey([]byte("another secret"))
	l := Link{ID: "shl_A", HasPassphrase: true, passHash: enc}
	now := time.Now()
	proof := MakeProof(key, l, now)
	if !CheckProof(key, l, proof, now.Add(time.Hour)) {
		t.Error("a fresh proof")
	}
	if CheckProof(key, l, proof, now.Add(9*time.Hour)) {
		t.Error("an old proof")
	}
	if CheckProof(other, l, proof, now) || CheckProof(key, Link{ID: "shl_B", HasPassphrase: true, passHash: enc}, proof, now) || CheckProof(key, Link{ID: "shl_A", HasPassphrase: true, passHash: enc + "x"}, proof, now) {
		t.Error("a proof for another key, link or passphrase")
	}
	for _, junk := range []string{"", "x", proof[:10], proof + "A"} {
		if CheckProof(key, l, junk, now) {
			t.Errorf("%q opened the link", junk)
		}
	}
	if !CheckProof(key, Link{ID: "shl_C"}, "", now) {
		t.Error("a link with no passphrase needs no proof")
	}
}

func TestCleanTakesOutEverythingPrivate(t *testing.T) {
	in := "---\nSECRETFRONT: 1\n---\nVisible one. %%SECRETNOTE hidden%% Visible two. <!-- SECRETCOMMENT -->\n\nA <scriobh-ghost>GHOSTED words</scriobh-ghost> stay or go.\n\n<!-- scriobh:alts {\"version\":1,\"items\":[]} -->\n"
	omit, ok1 := Clean(in, "omit")
	keep, ok2 := Clean(in, "keep")
	if !ok1 || !ok2 {
		t.Fatal("not placed")
	}
	for _, secret := range []string{"SECRETFRONT", "SECRETNOTE", "SECRETCOMMENT", "scriobh:alts", "<scriobh-ghost>", "</scriobh-ghost>"} {
		if strings.Contains(omit, secret) || strings.Contains(keep, secret) {
			t.Errorf("%q survived: %q / %q", secret, omit, keep)
		}
	}
	if strings.Contains(omit, "GHOSTED") || !strings.Contains(keep, "GHOSTED words") {
		t.Errorf("ghosts: omit %q keep %q", omit, keep)
	}
	if !strings.Contains(omit, "Visible one.") || !strings.Contains(omit, "Visible two.") {
		t.Errorf("visible text lost: %q", omit)
	}
}

// The download is built from the renderer's own reading of the text: it leaks nothing the reading view hides
// (a footnote whose only reference is in a private note), and keeps what the reading view shows, code included.
func TestCleanAgreesWithTheReadingView(t *testing.T) {
	in := "Open text %%a note[^1]%% goes on.\n\n[^1]: HIDDENFOOTNOTE body\n\n" +
		"```\nfenced %%not a note%% and <!-- not a comment -->\n```\n\n" +
		"Inline `%%code%% span` here.\n\n" +
		"A %%note that\n\nspans paragraphs%% end.\n"
	got, ok := Clean(in, "omit")
	if !ok {
		t.Fatal("not placed")
	}
	if strings.Contains(got, "HIDDENFOOTNOTE") {
		t.Errorf("a hidden footnote leaked: %q", got)
	}
	for _, kept := range []string{"fenced %%not a note%% and <!-- not a comment -->", "`%%code%% span`"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q was cut from code: %q", kept, got)
		}
	}
	// an unpaired marker across paragraphs is text in the reading view, so it is text here
	if !strings.Contains(got, "A %%note that") || !strings.Contains(got, "spans paragraphs%% end.") {
		t.Errorf("markers the reading view shows were cut: %q", got)
	}
}
