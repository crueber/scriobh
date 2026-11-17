package share

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// What a link shows: the manuscript's reading text. Documents of kind note and
// research are never shown, nor anything beneath them; a link that names documents
// shows those and their descendants; trashed documents are not shown.

// DocEntry is one document in the contents.
type DocEntry struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Title    string `json:"title"`
	Kind     string `json:"kind" enum:"part,chapter,scene,front_matter,back_matter"`
	Words    int    `json:"words"`
	Depth    int    `json:"depth"`
	Threads  int    `json:"threads,omitempty" desc:"Threads of this link on it (comment links)."`
}

// ManifestOut is what opens a link.
type ManifestOut struct {
	Title         string     `json:"title" desc:"The project's title."`
	Scope         string     `json:"scope" enum:"read,comment"`
	AllowDownload bool       `json:"allow_download"`
	Ghosts        string     `json:"ghosts" enum:"omit,keep"`
	ExpiresAt     string     `json:"expires_at,omitempty"`
	Documents     []DocEntry `json:"documents" desc:"In reading order."`
	Words         int        `json:"words"`
}

type node struct {
	docref.Node
	words   int
	compile bool
}

// covered loads the documents the link shows, in reading order.
func covered(ctx context.Context, d *db.DB, l Link) ([]DocEntry, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT id, COALESCE(parent_id,''), kind, title, position, word_count, include_in_compile FROM documents WHERE project_id = ? AND deleted_at IS NULL`, l.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []docref.Node
	byID := map[string]node{}
	for rows.Next() {
		var n node
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Position, &n.words, &n.compile); err != nil {
			return nil, err
		}
		nodes = append(nodes, n.Node)
		byID[n.ID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	order := docref.Order(nodes)
	roots := map[string]bool{}
	for _, r := range l.Roots {
		roots[r] = true
	}
	skip, in := map[string]bool{}, map[string]bool{}
	depth := map[string]int{}
	var out []DocEntry
	for _, id := range order {
		n := byID[id]
		// Notes and research are never shown, nor is a document the writer left out of the compile (a
		// scratch draft, a cut chapter) or anything beneath one. ai_exclude is about AI only and hides nothing here.
		if n.Kind == "note" || n.Kind == "research" || !n.compile || skip[n.ParentID] {
			skip[id] = true
			continue
		}
		parentIn := in[n.ParentID]
		if l.Whole || roots[id] || parentIn {
			in[id] = true
		} else {
			continue
		}
		depth[id] = depth[n.ParentID] + 1
		if _, ok := byID[n.ParentID]; !ok || !in[n.ParentID] {
			depth[id] = 0
		}
		out = append(out, DocEntry{ID: id, ParentID: n.ParentID, Title: n.Title, Kind: n.Kind, Words: n.words, Depth: depth[id]})
	}
	for i := range out {
		if _, ok := in[out[i].ParentID]; !ok {
			out[i].ParentID = ""
		}
	}
	return out, nil
}

// Covers reports whether the link shows a document.
func Covers(ctx context.Context, d *db.DB, l Link, docID string) (bool, error) {
	ents, err := covered(ctx, d, l)
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.ID == docID {
			return true, nil
		}
	}
	return false, nil
}

// Manifest is the contents of the link, with how many threads of its own it has on each.
func Manifest(ctx context.Context, d *db.DB, l Link) (ManifestOut, error) {
	ents, err := covered(ctx, d, l)
	if err != nil {
		return ManifestOut{}, err
	}
	out := ManifestOut{Title: l.ProjectTitle, Scope: l.Scope, AllowDownload: l.AllowDownload, Ghosts: l.Ghosts, ExpiresAt: l.ExpiresAt, Documents: ents}
	if out.Documents == nil {
		out.Documents = []DocEntry{}
	}
	counts := map[string]int{}
	if l.CanComment() {
		rows, err := d.Read().QueryContext(ctx, `SELECT document_id, count(*) FROM comments WHERE share_id = ? AND parent_id IS NULL AND deleted_at IS NULL AND document_id IS NOT NULL GROUP BY document_id`, l.ID)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return out, err
			}
			counts[id] = n
		}
		rows.Close()
	}
	for i := range out.Documents {
		out.Documents[i].Threads = counts[out.Documents[i].ID]
		out.Words += out.Documents[i].Words
	}
	return out, nil
}

// DocOut is one document to read.
type DocOut struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Kind     string        `json:"kind"`
	Revision int           `json:"revision" desc:"Changes when the text does; comments name the text, not the revision."`
	Words    int           `json:"words"`
	HTML     string        `json:"html" desc:"The text as a fragment, rendered by a whitelist: only the elements the reading view needs, no scripts, no remote loads, images as notes. Private notes, comments, front matter and (unless the link keeps them) ghosted text are not in it. Safe to insert with innerHTML under the page's CSP."`
	Prev     string        `json:"prev_id,omitempty"`
	Next     string        `json:"next_id,omitempty"`
	Threads  []GuestThread `json:"threads,omitempty" desc:"The threads of this link on this document (comment links)."`
}

var errNoDoc = errors.New("share: no such document")

// ErrNoDocument is returned for a document the link does not show.
var ErrNoDocument = errNoDoc

// readableBody is the text a link serves for a document: the body before the
// alternatives annotation, with the ghost markers dealt with. Private notes and
// comments are dropped by the renderer (and by Clean, for the download).
func readableBody(content, ghosts string) string {
	return text.StripGhosts(comanchor.Body(content), ghosts != "keep")
}

// Document is one document of the link, rendered.
func Document(ctx context.Context, d *db.DB, l Link, docID string) (DocOut, error) {
	ents, err := covered(ctx, d, l)
	if err != nil {
		return DocOut{}, err
	}
	var out DocOut
	idx := -1
	for i, e := range ents {
		if e.ID == docID {
			idx = i
		}
	}
	if idx < 0 {
		return out, errNoDoc
	}
	var content string
	if err := d.Read().QueryRowContext(ctx, `SELECT content, revision FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, docID, l.ProjectID).Scan(&content, &out.Revision); err != nil {
		return out, errNoDoc
	}
	e := ents[idx]
	out.ID, out.Title, out.Kind, out.Words = e.ID, e.Title, e.Kind, e.Words
	if idx > 0 {
		out.Prev = ents[idx-1].ID
	}
	if idx+1 < len(ents) {
		out.Next = ents[idx+1].ID
	}
	out.HTML = text.RenderHTML(readableBody(content, l.Ghosts), text.HTMLOptions{IDPrefix: "d" + shortID(docID) + "-", HeadingShift: 1})
	if l.CanComment() {
		if out.Threads, err = Threads(ctx, d, l, docID, content); err != nil {
			return out, err
		}
	}
	return out, nil
}

func shortID(id string) string {
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

// ---- the guest's view of comments ----------------------------------------------------------------

// GuestComment is one comment as a guest sees it.
type GuestComment struct {
	ID        string `json:"id"`
	Author    string `json:"author" desc:"Guest: Name for a guest of a link, the member's name for a reply from the writer's side."`
	Mine      bool   `json:"mine" desc:"Written through this link."`
	Body      string `json:"body" desc:"Plain text; show it as text."`
	CreatedAt string `json:"created_at"`
}

// GuestThread is a thread started through the link.
type GuestThread struct {
	ID         string         `json:"id"`
	DocumentID string         `json:"document_id"`
	Status     string         `json:"status" enum:"open,resolved"`
	Anchor     string         `json:"anchor" enum:"active,orphaned,none" desc:"none: on the document as a whole."`
	Quote      string         `json:"quote,omitempty" desc:"The commented text as rendered (no formatting marks), when its place is known."`
	Before     string         `json:"before,omitempty" desc:"Up to 24 characters of rendered text before it, to tell repeats apart."`
	After      string         `json:"after,omitempty"`
	Comments   []GuestComment `json:"comments" desc:"The first comment, then the replies, oldest first."`
}

// Threads lists the threads started through the link on a document. content is
// the document's text now (for the plain anchors; private notes and ghosted text
// are never in them).
func Threads(ctx context.Context, d *db.DB, l Link, docID, content string) ([]GuestThread, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT c.id, c.status, c.anchor_state, c.anchor_start, c.anchor_end, c.anchor_quote FROM comments c
		WHERE c.share_id = ? AND c.document_id = ? AND c.parent_id IS NULL AND (c.deleted_at IS NULL OR EXISTS (SELECT 1 FROM comments r WHERE r.parent_id = c.id AND r.deleted_at IS NULL))
		ORDER BY (c.anchor_state = 'orphaned'), (c.anchor_state = 'none') DESC, c.anchor_start, c.id LIMIT 500`, l.ID, docID)
	if err != nil {
		return nil, err
	}
	var out []GuestThread
	type pos struct {
		start, end int
		quote      string
	}
	at := map[string]pos{}
	for rows.Next() {
		var t GuestThread
		var p pos
		if err := rows.Scan(&t.ID, &t.Status, &t.Anchor, &p.start, &p.end, &p.quote); err != nil {
			rows.Close()
			return nil, err
		}
		t.DocumentID = docID
		t.Comments = []GuestComment{}
		at[t.ID] = p
		out = append(out, t)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []GuestThread{}, nil
	}
	index := map[string]int{}
	ids := make([]any, len(out))
	for i, t := range out {
		index[t.ID] = i
		ids[i] = t.ID
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	crows, err := d.Read().QueryContext(ctx, `SELECT c.id, COALESCE(c.parent_id, c.id), c.share_id IS NOT NULL, c.guest_name, COALESCE(NULLIF(u.display_name,''), 'Member'), c.author_id IS NULL, c.body, c.created_at
		FROM comments c LEFT JOIN users u ON u.id = c.author_id
		WHERE (c.id IN (`+ph+`) OR c.parent_id IN (`+ph+`)) AND c.deleted_at IS NULL ORDER BY c.created_at, c.id`, append(ids, ids...)...)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var id, root, guest, name, body, created string
		var isGuest, noAuthor bool
		if err := crows.Scan(&id, &root, &isGuest, &guest, &name, &noAuthor, &body, &created); err != nil {
			return nil, err
		}
		gc := GuestComment{ID: id, Body: body, CreatedAt: created}
		switch {
		case isGuest:
			gc.Author, gc.Mine = "Guest: "+guest, true
		case noAuthor:
			gc.Author = "Removed user"
		default:
			gc.Author = name
		}
		out[index[root]].Comments = append(out[index[root]].Comments, gc)
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}
	// plain anchors: only for a place that is still where it was said to be
	body := comanchor.Body(content)
	hinter := comanchor.NewHinter(body)
	ix := alts.NewIndex(body)
	for i := range out {
		p := at[out[i].ID]
		if out[i].Anchor != comanchor.StateActive {
			continue
		}
		a, ok1 := ix.Byte(p.start)
		b, ok2 := ix.Byte(p.end)
		if !ok1 || !ok2 || a >= b || b > len(body) || body[a:b] != p.quote {
			continue
		}
		h := hinter.For(a, b)
		out[i].Quote, out[i].Before, out[i].After = h.Quote, h.Before, h.After
	}
	// a thread whose first comment was deleted but has live replies still shows
	kept := out[:0]
	for _, t := range out {
		if len(t.Comments) > 0 {
			kept = append(kept, t)
		}
	}
	return kept, nil
}

// ThreadsOf lists the threads of the link on a document the link shows.
func ThreadsOf(ctx context.Context, d *db.DB, l Link, docID string) ([]GuestThread, error) {
	ok, err := Covers(ctx, d, l, docID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNoDoc
	}
	var content string
	if err := d.Read().QueryRowContext(ctx, `SELECT content FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, docID, l.ProjectID).Scan(&content); err != nil {
		return nil, errNoDoc
	}
	return Threads(ctx, d, l, docID, content)
}

// RateError says the guests of a link have written enough for now.
type RateError struct{ RetryAfter time.Duration }

func (e *RateError) Error() string { return "share: too many comments" }

// GuestIn is a guest's comment.
type GuestIn struct {
	DocumentID string `json:"document_id" desc:"The document commented on."`
	ParentID   string `json:"parent_id,omitempty" desc:"Reply in this thread (one started through this link)."`
	Selection  string `json:"selection,omitempty" desc:"The text selected, as the page shows it. Empty: a comment on the document as a whole."`
	Before     string `json:"before,omitempty" desc:"Some of the text just before the selection, as shown, to tell repeats apart."`
	After      string `json:"after,omitempty" desc:"Some of the text just after it."`
	Body       string `json:"body" desc:"The comment, up to 2,000 characters. Plain text."`
	Name       string `json:"name" desc:"The name to write under, up to 60 characters. It is shown to the writer as Guest: Name."`
}

// GuestSink is what a guest's write needs of the event store: writing events in the
// transaction and waking the listeners after it.
type GuestSink interface {
	FlushTx(tx *sql.Tx, actorID string, events []ops.Event, audit []ops.AuditEntry) error
	Wake()
}

// GuestResult is the comment written.
type GuestResult struct {
	Thread    GuestThread `json:"thread"`
	Duplicate bool        `json:"duplicate,omitempty" desc:"The same comment (same name, text and place) was written through this link in the last minute: this is that one, and nothing new was written."`
}

// duplicateWindow is how long the same comment written again is taken for a double submit.
const duplicateWindow = time.Minute

func clientKey(in GuestIn) string {
	h := sha256.New()
	for _, f := range []string{in.DocumentID, in.ParentID, in.Name, in.Body, in.Selection, in.Before, in.After} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AddComment writes a guest's comment through the same InsertComment a member's
// comment.create uses. The caller has authenticated the link and opened it (the
// passphrase, if any); this checks the scope, that the link shows the document,
// and the limits of the link: an hour's comments, counting every attempt that failed (a stranger
// feeding it guesses spends it), and the total. The same comment sent again within a minute is a
// double submit, answered with the one that was written.
func AddComment(ctx context.Context, d *db.DB, m *Meter, sink GuestSink, l Link, in GuestIn, perHour int) (GuestResult, error) {
	var res GuestResult
	if !l.CanComment() {
		return res, ops.Forbidden("This link is for reading only.")
	}
	now := db.NowTime()
	since := db.Format(now.Add(-time.Hour))
	if n, oldest := m.Fails(l.ID, now.Add(-time.Hour)); n > 0 {
		var recent int
		if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE share_id = ? AND created_at > ?`, l.ID, since).Scan(&recent); err != nil {
			return res, err
		}
		if recent+n >= perHour {
			return res, &RateError{RetryAfter: retryAfter(oldest, now)}
		}
	}
	res, err := addComment(ctx, d, sink, l, in, perHour)
	if err != nil {
		var rate *RateError
		var oe *ops.Error
		if !errors.As(err, &rate) && errors.As(err, &oe) && oe.Code != ops.CodeInternal {
			m.Fail(l.ID, now) // a refused attempt is an attempt
		}
	}
	return res, err
}

func retryAfter(oldest, now time.Time) time.Duration {
	if w := oldest.Add(time.Hour).Sub(now); w > 0 {
		return w
	}
	return time.Minute
}

func addComment(ctx context.Context, d *db.DB, sink GuestSink, l Link, in GuestIn, perHour int) (GuestResult, error) {
	var res GuestResult
	if in.DocumentID == "" {
		return res, ops.Invalid("document_id", "Say which document you are commenting on.")
	}
	ok, err := Covers(ctx, d, l, in.DocumentID)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ops.NotFound("document")
	}
	var anchor *comanchor.Range
	if strings.TrimSpace(in.Selection) != "" {
		anchor = &comanchor.Range{Quote: in.Selection, Before: in.Before, After: in.After, Plain: true}
	}
	key := clientKey(in)
	var created review.Created
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		res.Duplicate = false
		var dup string
		cut := db.Format(db.NowTime().Add(-duplicateWindow))
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(parent_id, id) FROM comments WHERE share_id = ? AND client_key = ? AND created_at > ? AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1`, l.ID, key, cut).Scan(&dup); err == nil {
			created.ThreadID, res.Duplicate = dup, true
			return nil
		}
		var total, recent int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE share_id = ?`, l.ID).Scan(&total); err != nil {
			return err
		}
		if total >= MaxGuestCommentsPerLink {
			return ops.Conflict("This link has taken the most comments it can.")
		}
		since := db.Format(db.NowTime().Add(-time.Hour))
		var oldest sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT count(*), min(created_at) FROM comments WHERE share_id = ? AND created_at > ?`, l.ID, since).Scan(&recent, &oldest); err != nil {
			return err
		}
		if recent >= perHour {
			t, _ := db.Parse(oldest.String)
			return &RateError{RetryAfter: retryAfter(t, db.NowTime())}
		}
		cr, evs, err := review.InsertComment(ctx, tx, review.NewComment{ProjectID: l.ProjectID, DocumentID: in.DocumentID, ParentID: in.ParentID, Body: in.Body,
			Anchor: anchor, Actor: review.Actor{ShareID: l.ID, GuestName: in.Name}, ClientKey: key})
		if err != nil {
			// Why a selection cannot be placed is told to a member, who can read the notes and
			// the ghosted text; to a guest every reason is the same words, or the reasons would
			// tell them whether text they guess at is in a private note.
			var oe *ops.Error
			if errors.As(err, &oe) && oe.Code == ops.CodeInvalid && oe.Field == "quote" {
				return ops.Invalid("selection", "That selection could not be placed. Select a stretch of ordinary text inside one paragraph, or comment on the document as a whole.")
			}
			return err
		}
		created = cr
		return sink.FlushTx(tx, "", evs, nil)
	})
	if err != nil {
		return res, err
	}
	if !res.Duplicate {
		sink.Wake()
	}
	var content string
	_ = d.Read().QueryRowContext(ctx, `SELECT content FROM documents WHERE id = ?`, in.DocumentID).Scan(&content)
	ths, err := Threads(ctx, d, l, in.DocumentID, content)
	if err != nil {
		return res, err
	}
	for _, t := range ths {
		if t.ID == created.ThreadID {
			res.Thread = t
		}
	}
	return res, nil
}

// ---- download ------------------------------------------------------------------------------------

// Clean is a document's Markdown as a link may hand it out: ghosted text as the link says, and then
// exactly what the reading view leaves out cut out (front matter, private notes, HTML comments,
// footnotes referenced only from a private note: text.CleanMarkdown, which is built on the
// renderer's own parse), every other byte kept, code included. It is NOT the Markdown export, which
// keeps all of those verbatim. ok is false when the text could not be placed; nothing is then handed out.
func Clean(content, ghosts string) (string, bool) {
	return text.CleanMarkdown(text.StripGhosts(comanchor.Body(content), ghosts != "keep"))
}

// Downloads builds and keeps the downloads of links: an answer is kept for a minute by what it was built from
// (the link, its ghost rule and the documents' hashes), and at most two are built at once, so a stranger with
// a link cannot make the server parse a whole book over and over.
type Downloads struct {
	mu      sync.Mutex
	entries map[string]dlEntry
	sem     chan struct{}
	// Builds counts the downloads actually built, for tests and diagnostics.
	Builds atomic.Int64
}

type dlEntry struct {
	name, body string
	at         time.Time
}

const (
	downloadTTL     = time.Minute
	downloadEntries = 16
	downloadMaxKept = 4 << 20
)

// NewDownloads makes an empty set.
func NewDownloads() *Downloads {
	return &Downloads{entries: map[string]dlEntry{}, sem: make(chan struct{}, 2)}
}

// Get is the download of a link.
func (dl *Downloads) Get(ctx context.Context, d *db.DB, l Link) (string, string, error) {
	ents, err := covered(ctx, d, l)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	h.Write([]byte(l.ID + "\x00" + l.Ghosts + "\x00" + l.ProjectTitle + "\x00"))
	hashes := map[string]string{}
	rows, err := d.Read().QueryContext(ctx, `SELECT id, content_hash FROM documents WHERE project_id = ? AND deleted_at IS NULL`, l.ProjectID)
	if err != nil {
		return "", "", err
	}
	for rows.Next() {
		var id, ch string
		if err := rows.Scan(&id, &ch); err != nil {
			rows.Close()
			return "", "", err
		}
		hashes[id] = ch
	}
	rows.Close()
	for _, e := range ents {
		h.Write([]byte(e.ID + "\x00" + e.Title + "\x00" + hashes[e.ID] + "\x00"))
	}
	key := hex.EncodeToString(h.Sum(nil))
	now := db.NowTime()
	dl.mu.Lock()
	if e, ok := dl.entries[key]; ok && now.Sub(e.at) < downloadTTL {
		dl.mu.Unlock()
		return e.name, e.body, nil
	}
	dl.mu.Unlock()
	select {
	case dl.sem <- struct{}{}:
		defer func() { <-dl.sem }()
	default:
		return "", "", ErrBusy
	}
	dl.Builds.Add(1)
	name, body, err := buildDownload(ctx, d, l, ents)
	if err != nil {
		return "", "", err
	}
	if len(body) <= downloadMaxKept {
		dl.mu.Lock()
		if len(dl.entries) >= downloadEntries {
			oldest, at := "", now
			for k, e := range dl.entries {
				if oldest == "" || e.at.Before(at) {
					oldest, at = k, e.at
				}
			}
			delete(dl.entries, oldest)
		}
		dl.entries[key] = dlEntry{name, body, now}
		dl.mu.Unlock()
	}
	return name, body, nil
}

func buildDownload(ctx context.Context, d *db.DB, l Link, ents []DocEntry) (string, string, error) {
	var b strings.Builder
	b.WriteString("# " + oneLine(l.ProjectTitle) + "\n")
	for _, e := range ents {
		var content string
		if err := d.Read().QueryRowContext(ctx, `SELECT content FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, e.ID, l.ProjectID).Scan(&content); err != nil {
			continue
		}
		b.WriteString("\n## " + oneLine(e.Title) + "\n\n")
		clean, ok := Clean(content, l.Ghosts)
		if !ok {
			clean = "*(This part could not be prepared for download.)*"
		}
		b.WriteString(strings.TrimSpace(clean))
		b.WriteString("\n")
	}
	return slug(l.ProjectTitle) + ".md", b.String(), nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "manuscript"
	}
	return out
}
