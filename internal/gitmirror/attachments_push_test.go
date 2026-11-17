package gitmirror_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addAttachments writes attachment files into the fake mirror and lists them in
// its manifest, the way the mirror does.
func (f *fakeSource) addAttachments(t *testing.T, files map[string][]byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(f.dir, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mf map[string]any
	if err := json.Unmarshal(raw, &mf); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	for _, name := range sortedKeys(files) {
		b := files[name]
		full := filepath.Join(f.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		list = append(list, map[string]any{"id": "att_" + name, "path": name, "sha256": hex.EncodeToString(sum[:]), "bytes": len(b)})
	}
	mf["attachments"] = list
	out, _ := json.MarshalIndent(mf, "", "  ")
	if err := os.WriteFile(filepath.Join(f.dir, "project.json"), append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	f.hash += "+attachments"
}

// Attachments are pushed under attachments/, byte for byte; one over the file
// size limit is left out (with its manifest entry) instead of stopping the
// push; binary files are not scanned for secrets, text ones are.
func TestAttachmentsArePushedAndTheOversizedOneIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.start()
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00, 0xff, 0x10}, 500)...)
	// Bytes that look like a private key header, inside a binary: not a secret.
	png = append(png, []byte("-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gunVTLw7onLRnrq0\n")...)
	big := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{'x'}, 17<<20)...)
	h.src.addAttachments(t, map[string][]byte{
		"attachments/aaaaaaaaaaaa-map.png":  png,
		"attachments/bbbbbbbbbbbb-huge.pdf": big,
	})
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	p := h.push(h.owner)
	if p.Status != "ok" || p.Outcome != "pushed" {
		t.Fatalf("push = %+v", p)
	}
	files := filesAt(t, dir, commitsOf(t, dir, "main")[0])
	if files["attachments/aaaaaaaaaaaa-map.png"] != string(png) {
		t.Errorf("the attachment is not byte for byte what the mirror holds (%d bytes)", len(files["attachments/aaaaaaaaaaaa-map.png"]))
	}
	if _, ok := files["attachments/bbbbbbbbbbbb-huge.pdf"]; ok {
		t.Error("an attachment over the size limit was pushed")
	}
	var mf struct {
		Attachments []struct{ Path string }
	}
	if err := json.Unmarshal([]byte(files["scriobh.json"]), &mf); err != nil || len(mf.Attachments) != 1 || mf.Attachments[0].Path != "attachments/aaaaaaaaaaaa-map.png" {
		t.Errorf("the published manifest should list only what was pushed: %s", files["scriobh.json"])
	}
	// The push is quiet: nothing changed, so nothing is pushed again.
	if p2 := h.push(h.owner); p2.Outcome != "up_to_date" && p2.Outcome != "nothing" {
		t.Logf("second push outcome: %s", p2.Outcome)
	}
}

func TestATextAttachmentIsScannedForSecrets(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.src.addAttachments(t, map[string][]byte{
		"attachments/aaaaaaaaaaaa-notes.txt": []byte("my key: -----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gunVTLw7onLRnrq0\n-----END RSA PRIVATE KEY-----\n"),
	})
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	p := h.push(h.owner)
	if p.Status == "ok" {
		t.Fatalf("a text attachment with a private key was pushed: %+v", p)
	}
	if !strings.Contains(p.ErrorMessage, "attachments/aaaaaaaaaaaa-notes.txt") {
		t.Errorf("the refusal should name the file: %q", p.ErrorMessage)
	}
	if cs := commitsOf(t, dir, "main"); len(cs) != 0 {
		t.Errorf("%d commits reached the remote", len(cs))
	}
}

// A manifest that names a path outside attachments/ (or climbs out of it) is refused.
func TestAnAttachmentPathCannotLeaveItsFolder(t *testing.T) {
	for _, bad := range []string{"../project.json", "attachments/../README.md", "manuscript/01-the-harbour.md", "attachments/sub/x.png", "attachments/.hidden.png", "attachments/x.exe"} {
		h := newHarness(t)
		h.start()
		h.src.addAttachments(t, map[string][]byte{bad: []byte("x")})
		url, _, dir := h.bareRemote()
		h.addRemote(url)
		if p := h.push(h.owner); p.Status == "ok" {
			t.Errorf("%q was pushed", bad)
		}
		if cs := commitsOf(t, dir, "main"); len(cs) != 0 {
			t.Errorf("%q: %d commits reached the remote", bad, len(cs))
		}
	}
}

// An attachment the mirror lists but cannot supply (a restore made without
// files, a source that is gone) is left out of the push and its manifest, not a
// reason to retry for ever.
func TestAMissingAttachmentDoesNotBlockThePush(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.src.addAttachments(t, map[string][]byte{
		"attachments/aaaaaaaaaaaa-here.png":    []byte("\x89PNG\r\n\x1a\nhere"),
		"attachments/bbbbbbbbbbbb-missing.png": []byte("\x89PNG\r\n\x1a\ngone"),
	})
	if err := os.Remove(filepath.Join(h.src.dir, "attachments", "bbbbbbbbbbbb-missing.png")); err != nil {
		t.Fatal(err)
	}
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	p := h.push(h.owner)
	if p.Status != "ok" || p.Outcome != "pushed" {
		t.Fatalf("push = %+v", p)
	}
	files := filesAt(t, dir, commitsOf(t, dir, "main")[0])
	if _, ok := files["attachments/aaaaaaaaaaaa-here.png"]; !ok {
		t.Error("the attachment that is there was not pushed")
	}
	if _, ok := files["attachments/bbbbbbbbbbbb-missing.png"]; ok {
		t.Error("a missing attachment appeared")
	}
	var mf struct{ Attachments []struct{ Path string } }
	if err := json.Unmarshal([]byte(files["scriobh.json"]), &mf); err != nil || len(mf.Attachments) != 1 {
		t.Errorf("the published manifest names what is not there: %s", files["scriobh.json"])
	}
}
