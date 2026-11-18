package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
)

// Save now, for the desktop shell (docs/UPDATES.md, "Restart to Update"). Before the shell stops the server to install an
// update it asks every open writing page to save what it holds, and waits until none is left with unsaved text. The page
// saves in its ordinary way (js/doc.js flushAll, the same request a keystroke's autosave makes); nothing here writes text,
// and nothing here is an op, because the shell has no principal: it proves itself with a key derived from the launch token.
//
//	POST /desktop/save-now          the shell. Starts a round: every writing page that holds a live stream is sent an
//	                                event `desktop.save` and answers {"n": round, "pages": how many were asked}.
//	GET  /desktop/save-status?n=N   the shell. {"n", "pending": pages that have not answered, "unsaved": pages that
//	                                answered that they still hold text, "gone": pages whose stream closed before they
//	                                answered (nobody confirmed that their text was saved)}.
//	POST /desktop/saved             a page, with the launch cookie and the id its event carried: {n, flusher, saved}.
//
// Only a writing page is asked: its live marker carries `data-flush`, js/live.js adds `flush=1` to the stream's address,
// and the stream registers here. A page whose stream has closed (hidden, left) is not waited for, but it is
// counted as "gone" and never as saved (the shell tells the person that nothing confirmed it): leaving a page saves it, and
// what cannot be sent is kept in the page's local draft, offered back only while the app's address is the same. A hidden window has no stream, so "nobody was asked" is
// not "everything is saved": the shell is told how many were asked.

type saveHub struct {
	mu       sync.Mutex
	round    int64
	flushers map[string]*flusher
	// answers of the latest round, by flusher: 0 asked, 1 saved, 2 still holds text, 3 its stream closed before it answered
	answers map[string]int
}

// flusher is one open writing page's stream.
type flusher struct {
	id   string
	wake chan int64 // holds at most the latest round
}

func newSaveHub() *saveHub {
	return &saveHub{flushers: map[string]*flusher{}, answers: map[string]int{}}
}

// register adds a stream. The returned function removes it when the stream ends; a page that has gone without answering
// is no longer waited for.
func (h *saveHub) register() (*flusher, func()) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("server: entropy source failed")
	}
	f := &flusher{id: hex.EncodeToString(raw[:]), wake: make(chan int64, 1)}
	h.mu.Lock()
	h.flushers[f.id] = f
	h.mu.Unlock()
	return f, func() {
		h.mu.Lock()
		delete(h.flushers, f.id)
		// Not waited for (leaving a page saves it), but not forgotten either: nobody confirmed that its text was saved, and the
		// shell says so to the person instead of counting it as saved.
		if a, asked := h.answers[f.id]; asked && a == 0 {
			h.answers[f.id] = 3
		}
		h.mu.Unlock()
	}
}

// ask starts a round: every registered page is woken with its number.
func (h *saveHub) ask() (n int64, pages int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.round++
	h.answers = map[string]int{}
	for id, f := range h.flushers {
		h.answers[id] = 0
		select {
		case <-f.wake: // an unread earlier round is replaced by this one
		default:
		}
		f.wake <- h.round
	}
	return h.round, len(h.flushers)
}

// answer records a page's answer for a round. It reports whether the page and the round were the current ones.
func (h *saveHub) answer(n int64, id string, saved bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n != h.round {
		return false
	}
	if _, asked := h.answers[id]; !asked {
		return false
	}
	if saved {
		h.answers[id] = 1
	} else {
		h.answers[id] = 2
	}
	return true
}

// status counts the latest round. ok is false for a round that is not the latest.
func (h *saveHub) status(n int64) (pending, unsaved, gone int, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n != h.round {
		return 0, 0, 0, false
	}
	for _, a := range h.answers {
		switch a {
		case 0:
			pending++
		case 2:
			unsaved++
		case 3:
			gone++
		}
	}
	return pending, unsaved, gone, true
}

func writeSaveJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// desktopSaveNow is POST /desktop/save-now.
func (s *Server) desktopSaveNow(w http.ResponseWriter, r *http.Request) {
	if !s.desk.launch.ShellOK(r) {
		s.notFound(w, r)
		return
	}
	n, pages := s.desk.save.ask()
	writeSaveJSON(w, http.StatusOK, map[string]any{"n": n, "pages": pages})
}

// desktopSaveStatus is GET /desktop/save-status?n=N.
func (s *Server) desktopSaveStatus(w http.ResponseWriter, r *http.Request) {
	if !s.desk.launch.ShellOK(r) {
		s.notFound(w, r)
		return
	}
	n, err := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
	if err != nil {
		writeSaveJSON(w, http.StatusBadRequest, map[string]any{"error": "n is the round number save-now answered"})
		return
	}
	pending, unsaved, gone, ok := s.desk.save.status(n)
	if !ok {
		writeSaveJSON(w, http.StatusConflict, map[string]any{"error": "that round is over; ask again"})
		return
	}
	writeSaveJSON(w, http.StatusOK, map[string]any{"n": n, "pending": pending, "unsaved": unsaved, "gone": gone})
}

// desktopSaved is POST /desktop/saved: a page says it has saved (or cannot). It needs the launch cookie (SameSite=Strict,
// so another site's request has none), a header a cross-origin page cannot add without a preflight this server never
// answers, and the id that only the page holding the stream was given.
func (s *Server) desktopSaved(w http.ResponseWriter, r *http.Request) {
	if !s.desk.launch.CookieOK(r) || r.Header.Get("X-Scriobh-Fetch") != "1" {
		s.notFound(w, r)
		return
	}
	var in struct {
		N       int64  `json:"n"`
		Flusher string `json:"flusher"`
		Saved   bool   `json:"saved"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeSaveJSON(w, http.StatusBadRequest, map[string]any{"error": "not a save answer"})
		return
	}
	if !s.desk.save.answer(in.N, in.Flusher, in.Saved) {
		writeSaveJSON(w, http.StatusConflict, map[string]any{"error": "not the current round or an unknown page"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
