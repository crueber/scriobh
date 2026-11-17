package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// TestMeasureCoverBodyMemory measures what the cover form's body costs the server while it is read, through a real
// listener and a body that is generated as it is sent (the client holds none of it). It does nothing unless
// SCRIOBH_MEASURE_COVER_BODY is set:
//
//	SCRIOBH_MEASURE_COVER_BODY=1 GOMAXPROCS=2 go test ./internal/server -run TestMeasureCoverBodyMemory -v -count=1
//
// Each scenario reports the largest the Go heap grew above its start while the posts ran (sampled every 2 ms) and the
// responses. The figures are in docs/COVERS.md and docs/ARCHITECTURE.md.
func TestMeasureCoverBodyMemory(t *testing.T) {
	if os.Getenv("SCRIOBH_MEASURE_COVER_BODY") == "" {
		t.Skip("set SCRIOBH_MEASURE_COVER_BODY to measure")
	}
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	member := s.newCaller(t, "member", ops.RoleWriter)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	pid := idOf(t, s.do(owner, "POST", "/api/v1/projects", `{"title":"Measured"}`).Body.Bytes())
	s.addMember(t, pid, member.userID, ops.ProjectEditor)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	url := srv.URL + "/ui/projects/" + pid + "/cover"
	per := int64(s.Cfg.UploadMaxMB) << 20
	total := 3*per - 1<<20 // a body just inside the route's limit: 74 MB at the default 25 MB
	t.Logf("upload limit %d MB; each body is %d MB", s.Cfg.UploadMaxMB, total>>20)

	post := func(c caller) (int, int64) {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			_ = mw.WriteField("_csrf", s.Sess.CSRFToken(sessionOf(t, s, c)))
			_ = mw.WriteField("shape", "sides")
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="front_file"; filename="junk.jpg"`)
			h.Set("Content-Type", "image/jpeg")
			w, _ := mw.CreatePart(h)
			chunk := bytes.Repeat([]byte{0xAB}, 1<<20)
			for n := int64(0); n < total; n += int64(len(chunk)) {
				if _, err := w.Write(chunk); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
			_ = mw.Close()
			_ = pw.Close()
		}()
		req, _ := http.NewRequest("POST", url, pr)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(c.cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			_ = pr.Close()
			return -1, 0
		}
		defer resp.Body.Close()
		n, _ := io.Copy(io.Discard, resp.Body)
		_ = pr.Close()
		return resp.StatusCode, n
	}
	run := func(name string, n int, c caller) {
		runtime.GC()
		var base runtime.MemStats
		runtime.ReadMemStats(&base)
		var peak, peakSys uint64
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			var m runtime.MemStats
			for {
				runtime.ReadMemStats(&m)
				peak, peakSys = max(peak, m.HeapAlloc), max(peakSys, m.Sys)
				select {
				case <-stop:
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
		}()
		start := time.Now()
		codes := make([]int, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); codes[i], _ = post(c) }()
		}
		wg.Wait()
		close(stop)
		<-done
		t.Logf("%-40s heap grew %4d MB (%.1fx one body), process memory from the system %4d MB, %v, statuses %v",
			name, (peak-min(peak, base.HeapAlloc))>>20, float64(peak-min(peak, base.HeapAlloc))/float64(total), (peakSys-min(peakSys, base.Sys))>>20, time.Since(start).Round(10*time.Millisecond), codes)
	}
	run("a non-member, one body", 1, outsider)
	run("eight non-members at once", 8, outsider)
	run("a member, one body", 1, member)
	run("eight members at once", 8, member)
}
