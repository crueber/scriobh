package server

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A link planted in the files folder (or a directory standing in for a blob)
// must not make the file route or the download op read what it points at.
func TestStoredFilesThatAreLinksAreNeverServed(t *testing.T) {
	f := newAttFixture(t, nil)
	a := decodeAtt(t, f.upload(f.owner, f.pid, "filename=a.png", pngBytes(t, 9, 9, 60), ""))
	dir := filepath.Join(f.s.Cfg.DataDir, "files", "projects", f.pid, "attachments")
	blob := filepath.Join(dir, a.Item.SHA256+".png")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(what string) {
		t.Helper()
		for _, path := range []string{a.Item.URL, "/api/v1/projects/" + f.pid + "/attachments/" + a.Item.ID + "/download"} {
			rec := f.get(f.owner, path)
			if rec.Code != 404 || strings.Contains(rec.Body.String(), "OUTSIDE SECRET") {
				t.Errorf("%s: %s answered %d %q", what, path, rec.Code, rec.Body.String())
			}
		}
	}
	good := f.get(f.owner, a.Item.URL)
	if good.Code != 200 {
		t.Fatalf("before: %d", good.Code)
	}
	// 1. The blob is a link to a file elsewhere.
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, blob); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	check("blob is a symlink")
	// 2. The blob is a link to a file inside the folder (another attachment's, say).
	_ = os.Remove(blob)
	other := filepath.Join(dir, strings.Repeat("0", 64)+".png")
	if err := os.WriteFile(other, good.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(other, blob)
	check("blob links to another file in the folder")
	// 3. The owner's folder is a link to a real directory holding the bytes.
	_ = os.Remove(blob)
	_ = os.Remove(other)
	real := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, a.Item.SHA256+".png"), good.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, dir); err != nil {
		t.Fatal(err)
	}
	check("the attachments folder is a link")
	// 4. A directory where the file should be.
	_ = os.Remove(dir)
	if err := os.MkdirAll(blob, 0o700); err != nil {
		t.Fatal(err)
	}
	check("a directory in place of the file")
	// And a real file there again is served.
	_ = os.RemoveAll(blob)
	if err := os.WriteFile(blob, good.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(f.owner, a.Item.URL); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), good.Body.Bytes()) {
		t.Errorf("a regular file stopped being served: %d", rec.Code)
	}
}

// A file that cannot fit is refused before it is read; the check in the
// transaction still decides the rest.
func TestUploadIsRefusedEarlyWhenItCannotFit(t *testing.T) {
	f := newAttFixture(t, map[string]string{"SCRIOBH_QUOTA_PROJECT_MB": "1"})
	if rec := f.upload(f.owner, f.pid, "filename=a.txt", append(bytes.Repeat([]byte{'a'}, 1<<20-1), 'x'), ""); rec.Code != 201 {
		t.Fatalf("filling the project: %d", rec.Code)
	}
	for _, length := range []int64{1 << 10, -1} { // declared, and not declared
		rd := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte{'b'}, 1<<10))}
		req := httptest.NewRequest("POST", "/api/v1/projects/"+f.pid+"/attachments?filename=b.txt", rd)
		req.ContentLength = length
		req.AddCookie(f.owner.cookie)
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, req)
		if rec.Code != 413 || !strings.Contains(rec.Body.String(), "quota_exceeded") || rd.n != 0 {
			t.Errorf("length %d: %d, %d bytes read: %s", length, rec.Code, rd.n, rec.Body)
		}
	}
	// A file declared larger than what is left is refused before reading too.
	g := newAttFixture(t, map[string]string{"SCRIOBH_QUOTA_PROJECT_MB": "1"})
	rd := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte{'c'}, 2<<20))}
	req := httptest.NewRequest("POST", "/api/v1/projects/"+g.pid+"/attachments?filename=c.txt", rd)
	req.ContentLength = 2 << 20
	req.AddCookie(g.owner.cookie)
	rec := httptest.NewRecorder()
	g.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 413 || rd.n != 0 {
		t.Errorf("a declared 2 MB into a 1 MB project: %d, %d bytes read", rec.Code, rd.n)
	}
}

// One person's uploads in flight are capped; someone else's are not affected.
func TestUploadsInFlightAreCappedPerPerson(t *testing.T) {
	f := newAttFixture(t, map[string]string{"SCRIOBH_UPLOAD_CONCURRENCY": "1"})
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest("POST", "/api/v1/projects/"+f.pid+"/attachments?filename=slow.txt", pr)
		req.ContentLength = -1
		req.AddCookie(f.owner.cookie)
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, req)
		done <- rec.Code
	}()
	_, _ = pw.Write([]byte("start of a slow upload "))
	deadline := time.Now().Add(10 * time.Second * sseFactor)
	code := 0
	for time.Now().Before(deadline) {
		code = f.upload(f.owner, f.pid, "filename=second.txt", []byte("second"), "").Code
		if code == 429 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != 429 {
		t.Fatalf("a second upload while one is in flight: %d, want 429", code)
	}
	if c := f.upload(f.editor, f.pid, "filename=other.txt", []byte("another person"), "").Code; c != 201 {
		t.Errorf("another person's upload: %d", c)
	}
	_ = pw.Close() // the slow one finishes
	select {
	case c := <-done:
		if c != 201 {
			t.Errorf("the slow upload ended with %d", c)
		}
	case <-time.After(10 * time.Second * sseFactor):
		t.Fatal("the slow upload never finished")
	}
	if c := f.upload(f.owner, f.pid, "filename=third.txt", []byte("third"), "").Code; c != 201 {
		t.Errorf("after it finished: %d", c)
	}
}

func webp(riffDelta int, chunk string, payload int) []byte {
	body := make([]byte, payload)
	size := 4 + 8 + payload // WEBP + chunk header + payload
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(size+riffDelta))
	b.WriteString("WEBP" + chunk)
	_ = binary.Write(&b, binary.LittleEndian, uint32(payload))
	b.Write(body)
	return b.Bytes()
}

// Each kind is recognised by its first structure, not by a prefix a forger can write.
func TestForgedAndTruncatedImagesAreRefused(t *testing.T) {
	f := newAttFixture(t, nil)
	good := pngBytes(t, 20, 20, 61)
	var g bytes.Buffer
	_ = gif.Encode(&g, image.NewPaletted(image.Rect(0, 0, 5, 5), color.Palette{color.Black, color.White}), nil)
	var j bytes.Buffer
	_ = jpeg.Encode(&j, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)
	for _, tc := range []struct {
		name, file string
		body       []byte
		want       int
	}{
		{"png signature and nothing else", "a.png", good[:8], 415},
		{"png signature then junk", "a.png", append(append([]byte{}, good[:8]...), bytes.Repeat([]byte("junk"), 20)...), 415},
		{"png whose first chunk is not IHDR", "a.png", append(append([]byte{}, good[:12]...), append([]byte("IDAT"), good[16:]...)...), 415},
		{"png cut inside its header", "a.png", good[:24], 415},
		{"a real png", "a.png", good, 201},
		{"jpeg start marker alone", "a.jpg", []byte("\xff\xd8\xff"), 415},
		{"jpeg start marker then no marker", "a.jpg", append([]byte("\xff\xd8\xff\x00"), bytes.Repeat([]byte{1}, 50)...), 415},
		{"jpeg marker then junk", "a.jpg", append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{1}, 50)...), 415},
		{"a real jpeg", "a.jpg", j.Bytes(), 201},
		{"gif header alone", "a.gif", []byte("GIF89a"), 415},
		{"gif header with a zero-size screen", "a.gif", append([]byte("GIF89a"), make([]byte, 20)...), 415},
		{"a real gif", "a.gif", g.Bytes(), 201},
		{"webp: header only", "a.webp", []byte("RIFF\x04\x00\x00\x00WEBP"), 415},
		{"webp: size field disagrees", "a.webp", webp(10, "VP8L", 10), 415},
		{"webp: unknown first chunk", "a.webp", webp(0, "JUNK", 10), 415},
		{"webp: a chunk longer than the file", "a.webp", func() []byte { b := webp(0, "VP8L", 10); b[16] = 0xff; return b }(), 415},
		{"webp: truncated", "a.webp", webp(0, "VP8L", 10)[:25], 415},
		{"webp: well formed", "a.webp", webp(0, "VP8L", 10), 201},
		{"pdf header alone", "a.pdf", []byte("%PDF-"), 415},
		{"pdf with no version", "a.pdf", []byte("%PDF-junk and more junk"), 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := f.upload(f.owner, f.pid, "filename="+tc.file, tc.body, ""); rec.Code != tc.want {
				t.Errorf("%d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}
