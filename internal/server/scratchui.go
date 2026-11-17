package server

import (
	"net/http"

	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/ops"
)

// The scratchpad page: one private pad per person per project and per Compendium. The ops read and write only
// the caller's own row, an administrator's included, and the pad is never exported, searched, mirrored, pushed
// or sent in an event. Membership of any role is all it needs: a reader may keep notes on a book they read.
// The page says all of that in plain words; the save itself is done by pad.js through the REST op (with a local
// draft), and by the plain form below when script is off.

type scratchData struct {
	resOwner
	CSRF     string
	Content  string
	Revision int
	Words    int
	Updated  string
	Error    string
	Conflict *scratchConflict
	Saved    bool
	DraftKey string
	MaxKB    int
	Zone     string
}

type scratchConflict struct {
	Theirs   string
	Revision int
}

func (s *Server) scratchView(r *http.Request, o resOwner) (scratchData, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"scratch.get", research.ScratchGetIn{ID: o.ID})
	if err != nil {
		return scratchData{}, err
	}
	sc := out.(research.ScratchItem).Item
	d := scratchData{resOwner: o, CSRF: authFrom(r.Context()).CSRF, Content: sc.Content, Revision: sc.Revision, Words: sc.WordCount, MaxKB: 256, Zone: s.userZone(r).String()}
	d.Updated = whenIn(sc.UpdatedAt, s.userZone(r))
	// A draft in this browser belongs to the person who typed it: their id is in the key, so whoever uses this browser
	// next never meets it, and signing out removes it.
	d.DraftKey = "scriobh:scratch:" + authFrom(r.Context()).P.UserID + ":" + o.Kind + ":" + o.ID
	return d, nil
}

func (s *Server) scratchPage(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resBySlug(w, r, kind)
		if !ok {
			return
		}
		d, err := s.scratchView(r, o)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Saved = r.URL.Query().Get("saved") == "1"
		w.Header().Set("Cache-Control", "no-store")
		s.render(w, r, http.StatusOK, "scratch", s.page(r, "Scratchpad · "+o.Name, d))
	}
}

// uiScratch saves the pad from the plain form. A pad someone else (another tab of the same person) saved since is
// shown beside the person's own text and nothing is overwritten.
func (s *Server) uiScratch(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resByID(w, r, kind)
		if !ok {
			return
		}
		content := r.PostFormValue("content")
		_, err := s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"scratch.set", research.ScratchSetIn{ID: o.ID, Content: content, BaseRevision: atoi(r.PostFormValue("rev"))})
		if err == nil {
			http.Redirect(w, r, o.link("/scratch", "saved", "1"), http.StatusSeeOther)
			return
		}
		e := ops.AsError(err)
		if e.Code != ops.CodeConflict && e.Code != ops.CodeInvalid && e.Code != ops.CodeTooLarge {
			s.fail(w, r, err)
			return
		}
		if isFetch(r) {
			s.fail(w, r, err)
			return
		}
		d, verr := s.scratchView(r, o)
		if verr != nil {
			s.fail(w, r, verr)
			return
		}
		status := http.StatusUnprocessableEntity
		if e.Code == ops.CodeConflict {
			status = http.StatusConflict
			d.Conflict = &scratchConflict{Theirs: d.Content, Revision: d.Revision}
			d.Error = "This pad was saved from another tab or device after you opened it. Your text is kept below. Choose which to keep."
		} else {
			d.Error = e.Message
		}
		d.Content = content
		s.render(w, r, status, "scratch", s.page(r, "Scratchpad · "+o.Name, d))
	}
}
