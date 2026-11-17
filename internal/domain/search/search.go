// Package search is full-text search over a project's documents.
//
// The index is an external-content FTS5 table (doc_fts) that follows doc_text,
// which the document ops keep in step with the document inside the same
// transaction. This package only reads it, plus Rebuild for repairs.
//
// User text never reaches FTS5 syntax: it is cut into words, every word is
// quoted, and the last one is a prefix match, so operators, column filters,
// parentheses and stray quotes in what someone types are only ever words.
package search

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"html"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/ops"
)

const (
	defaultLimit = 20
	maxLimit     = 50
	maxTokens    = 12
	maxTokenLen  = 64
	maxQueryLen  = 1000
)

// QueryIn is a search.
type QueryIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Q         string `query:"q" desc:"Words to look for. Every word must match; the last may be a prefix. Diacritics and case are ignored."`
	Limit     int    `query:"limit" desc:"Results per page: 1 to 50, default 20."`
	Offset    int    `query:"offset" desc:"Results to skip, for paging."`
}

// ProjectRef names the project the search runs in.
func (in QueryIn) ProjectRef() string { return in.ProjectID }

// Hit is one matching document.
type Hit struct {
	DocumentID  string `json:"document_id"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	MatchedIn   string `json:"matched_in" enum:"title,synopsis,body" desc:"The most important field that matched: title, then synopsis, then body."`
	Snippet     string `json:"snippet" desc:"Plain text around the first match: from the body when it matches, otherwise from the synopsis or title."`
	SnippetHTML string `json:"snippet_html" desc:"The same snippet as HTML: escaped, with each match in <mark>."`
	Offset      int    `json:"offset" desc:"Character offset of the first body match in the document's plain text (0 when the body does not match). The editor looks for the matched text to jump there."`
	WordCount   int    `json:"word_count"`
	UpdatedAt   string `json:"updated_at"`
}

// QueryOut is a page of results.
type QueryOut struct {
	Items      []Hit `json:"items"`
	Total      int   `json:"total" desc:"Every matching document, not just this page."`
	NextOffset int   `json:"next_offset,omitempty" desc:"Offset of the next page; absent on the last page."`
	Degraded   bool  `json:"degraded,omitempty" desc:"True when the index could not be read and a slower substring search answered instead. Run scriobh search rebuild."`
}

// Register adds the search op.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "search.query", Method: "GET", Path: "/api/v1/projects/{id}/search",
		Desc: "Search a project's documents: titles, synopses and text, best matches first.",
		Auth: ops.RequireProject(ops.ProjectReader)}, query)
	registerCompendium(r)
	registerResearch(r)
}

// match turns what someone typed into an FTS5 MATCH expression, or reports why
// it cannot. words is the same words, for the substring fallback.
func match(q string) (expr string, words []string, err error) {
	if len(q) > maxQueryLen {
		return "", nil, ops.Invalid("q", "That search is too long.")
	}
	var cur []rune
	hasWord, tooMany := false, false
	flush := func() {
		if len(cur) > 0 && hasWord {
			if len(words) >= maxTokens {
				tooMany = true
			}
			if len(cur) > maxTokenLen {
				cur = cur[:maxTokenLen]
			}
			if !tooMany {
				words = append(words, string(cur))
			}
		}
		cur, hasWord = cur[:0], false
	}
	for _, r := range q {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			cur = append(cur, r)
			hasWord = true
		case unicode.IsMark(r) && len(cur) > 0:
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	if tooMany {
		return "", nil, ops.Invalid("q", fmt.Sprintf("Search for at most %d words at a time.", maxTokens))
	}
	if len(words) == 0 {
		return "", nil, ops.Invalid("q", "Type a word to search for.")
	}
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = `"` + w + `"` // letters, digits and marks only: nothing to escape
	}
	parts[len(parts)-1] += "*"
	return strings.Join(parts, " "), words, nil
}

// marks are the highlight delimiters for one request: random private-use
// characters, so nothing in a document can forge them.
type marks struct{ start, end string }

func newMarks() marks {
	pick := func() string {
		var b strings.Builder
		for i := 0; i < 4; i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(0xF8FF-0xE000+1))
			if err != nil {
				n = big.NewInt(int64(i))
			}
			b.WriteRune(rune(0xE000 + n.Int64()))
		}
		return b.String()
	}
	return marks{start: pick(), end: pick()}
}

// render turns a marked-up snippet into plain text and escaped HTML.
func (m marks) render(s string) (plain, htmlOut string) {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	parts := strings.Split(s, m.start)
	var p, h strings.Builder
	p.WriteString(strings.ReplaceAll(parts[0], m.end, ""))
	h.WriteString(html.EscapeString(strings.ReplaceAll(parts[0], m.end, "")))
	for _, part := range parts[1:] {
		hit, rest, _ := strings.Cut(part, m.end)
		p.WriteString(hit)
		p.WriteString(rest)
		h.WriteString("<mark>" + html.EscapeString(hit) + "</mark>" + html.EscapeString(rest))
	}
	return p.String(), h.String()
}

func (m marks) has(s string) bool { return strings.Contains(s, m.start) }

func query(c *ops.Ctx, in QueryIn) (QueryOut, error) {
	expr, words, err := match(in.Q)
	if err != nil {
		return QueryOut{}, err
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if in.Offset < 0 {
		return QueryOut{}, ops.Invalid("offset", "The offset cannot be negative.")
	}
	out, err := ftsQuery(c, c.DB().Read(), in.ProjectID, expr, limit, in.Offset)
	if err != nil {
		if c.Err() != nil {
			return QueryOut{}, c.Err()
		}
		// The index could not be read (damaged or missing). Search still works,
		// slowly and without diacritic folding, until it is rebuilt.
		if c.Env.Log != nil {
			c.Env.Log.Warn("search index unreadable; answering with a substring search", "err", err)
		}
		out, err = likeQuery(c, c.DB().Read(), in.ProjectID, words, limit, in.Offset)
		if err != nil {
			return QueryOut{}, err
		}
		out.Degraded = true
	}
	if in.Offset+len(out.Items) < out.Total {
		out.NextOffset = in.Offset + len(out.Items)
	}
	return out, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// scope is the filter every search shares: the project the registry
// authorized, and no trashed documents.
const scope = ` d.project_id = ? AND t.project_id = ? AND d.deleted_at IS NULL`

func ftsQuery(ctx context.Context, q queryer, project, expr string, limit, offset int) (QueryOut, error) {
	out := QueryOut{Items: []Hit{}}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM doc_fts
		JOIN doc_text t ON t.id = doc_fts.rowid JOIN documents d ON d.id = t.document_id
		WHERE doc_fts MATCH ? AND`+scope, expr, project, project).Scan(&out.Total); err != nil {
		return QueryOut{}, err
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	// Step one: the page, in rank order. No snippets yet; computing them for
	// every match to sort would be the expensive part.
	rows, err := q.QueryContext(ctx, `SELECT t.id, d.id, d.title, d.kind, d.status, d.word_count, d.updated_at
		FROM doc_fts JOIN doc_text t ON t.id = doc_fts.rowid JOIN documents d ON d.id = t.document_id
		WHERE doc_fts MATCH ? AND`+scope+`
		ORDER BY bm25(doc_fts, 10.0, 5.0, 1.0), d.updated_at DESC, d.id
		LIMIT ? OFFSET ?`, expr, project, project, limit, offset)
	if err != nil {
		return QueryOut{}, err
	}
	var ids []any
	byRow := map[int64]*Hit{}
	for rows.Next() {
		var rowid int64
		var h Hit
		if err := rows.Scan(&rowid, &h.DocumentID, &h.Title, &h.Kind, &h.Status, &h.WordCount, &h.UpdatedAt); err != nil {
			rows.Close()
			return QueryOut{}, err
		}
		out.Items = append(out.Items, h)
		ids = append(ids, rowid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return QueryOut{}, err
	}
	rows.Close()
	for i := range out.Items {
		byRow[ids[i].(int64)] = &out.Items[i]
	}
	// Step two: snippets for just those rows, with markers no document can forge.
	m := newMarks()
	rows, err = q.QueryContext(ctx, `SELECT doc_fts.rowid,
			snippet(doc_fts, 0, ?1, ?2, '…', 16), snippet(doc_fts, 1, ?1, ?2, '…', 24), snippet(doc_fts, 2, ?1, ?2, '…', 24)
		FROM doc_fts WHERE doc_fts MATCH ?3 AND doc_fts.rowid IN (`+placeholders(len(ids), 4)+`)`,
		append([]any{m.start, m.end, expr}, ids...)...)
	if err != nil {
		return QueryOut{}, err
	}
	bodyMatched := map[int64]bool{}
	for rows.Next() {
		var rowid int64
		var title, syn, body string
		if err := rows.Scan(&rowid, &title, &syn, &body); err != nil {
			rows.Close()
			return QueryOut{}, err
		}
		h := byRow[rowid]
		if h == nil {
			continue
		}
		switch {
		case m.has(title):
			h.MatchedIn = "title"
		case m.has(syn):
			h.MatchedIn = "synopsis"
		default:
			h.MatchedIn = "body"
		}
		pick := body
		switch {
		case m.has(body):
			bodyMatched[rowid] = true
		case m.has(syn):
			pick = syn
		case m.has(title):
			pick = title
		}
		h.Snippet, h.SnippetHTML = m.render(pick)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return QueryOut{}, err
	}
	rows.Close()
	// Step three: where the first body match is, from the highlighted text.
	for rowid := range bodyMatched {
		var hl string
		if err := q.QueryRowContext(ctx, `SELECT highlight(doc_fts, 2, ?1, ?2) FROM doc_fts WHERE doc_fts MATCH ?3 AND doc_fts.rowid = ?4`,
			m.start, m.end, expr, rowid).Scan(&hl); err != nil {
			return QueryOut{}, err
		}
		if i := strings.Index(hl, m.start); i >= 0 {
			byRow[rowid].Offset = utf8.RuneCountInString(hl[:i])
		}
	}
	return out, nil
}

func placeholders(n, from int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("?")
		b.WriteString(strconv.Itoa(from + i))
	}
	return b.String()
}

// likeQuery is the fallback when the index cannot be read: every word must
// appear, case-insensitively, in the title, synopsis or text.
func likeQuery(ctx context.Context, q queryer, project string, words []string, limit, offset int) (QueryOut, error) {
	out := QueryOut{Items: []Hit{}}
	var conds []string
	args := []any{project, project}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for _, w := range words {
		conds = append(conds, `(t.title LIKE ? ESCAPE '\' OR t.synopsis LIKE ? ESCAPE '\' OR t.body LIKE ? ESCAPE '\')`)
		p := "%" + esc.Replace(w) + "%"
		args = append(args, p, p, p)
	}
	where := ` WHERE` + scope + ` AND ` + strings.Join(conds, " AND ")
	from := ` FROM doc_text t JOIN documents d ON d.id = t.document_id`
	if err := q.QueryRowContext(ctx, `SELECT count(*)`+from+where, args...).Scan(&out.Total); err != nil {
		return QueryOut{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT d.id, d.title, d.kind, d.status, d.word_count, d.updated_at, t.title, t.synopsis, t.body`+from+where+
		` ORDER BY d.updated_at DESC, d.id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return QueryOut{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var h Hit
		var title, syn, body string
		if err := rows.Scan(&h.DocumentID, &h.Title, &h.Kind, &h.Status, &h.WordCount, &h.UpdatedAt, &title, &syn, &body); err != nil {
			return QueryOut{}, err
		}
		fillFallback(&h, words, title, syn, body)
		out.Items = append(out.Items, h)
	}
	return out, rows.Err()
}

// foldIndex finds the first case-insensitive occurrence of word in text and
// returns its byte range in text itself. Lower-casing a rune can change its
// length in bytes, so the search runs over a lowered copy that remembers which
// rune each of its bytes came from.
func foldIndex(text, word string) (start, end int, ok bool) {
	lw := strings.ToLower(word)
	if lw == "" {
		return 0, 0, false
	}
	lower := make([]byte, 0, len(text))
	from := make([]int, 0, len(text))
	to := make([]int, 0, len(text))
	for o := 0; o < len(text); {
		r, size := utf8.DecodeRuneInString(text[o:])
		for _, b := range []byte(string(unicode.ToLower(r))) {
			lower, from, to = append(lower, b), append(from, o), append(to, o+size)
		}
		o += size
	}
	i := bytes.Index(lower, []byte(lw))
	if i < 0 {
		return 0, 0, false
	}
	return from[i], to[i+len(lw)-1], true
}

// fillFallback finds the first match for the substring search in Go.
func fillFallback(h *Hit, words []string, title, syn, body string) {
	// first is the earliest match of any word: its byte range in text.
	first := func(text string) (at, end int, ok bool) {
		for _, w := range words {
			if s, e, found := foldIndex(text, w); found && (!ok || s < at) {
				at, end, ok = s, e, true
			}
		}
		return at, end, ok
	}
	h.MatchedIn = "body"
	text := body
	if _, _, ok := first(title); ok {
		h.MatchedIn = "title"
	} else if _, _, ok := first(syn); ok {
		h.MatchedIn = "synopsis"
	}
	if _, _, ok := first(body); !ok {
		if h.MatchedIn == "synopsis" {
			text = syn
		} else {
			text = title
		}
	}
	at, matchEnd, ok := first(text)
	if !ok {
		h.Snippet = trim(text)
		h.SnippetHTML = html.EscapeString(h.Snippet)
		return
	}
	if text == body {
		h.Offset = utf8.RuneCountInString(body[:at])
	}
	from := at
	for back := 0; back < 60 && from > 0; back++ {
		_, size := utf8.DecodeLastRuneInString(text[:from])
		from -= size
	}
	to := matchEnd
	for fwd := 0; fwd < 120 && to < len(text); fwd++ {
		_, size := utf8.DecodeRuneInString(text[to:])
		to += size
	}
	pre, hit, post := text[from:at], text[at:matchEnd], text[matchEnd:to]
	h.Snippet = trim(pre + hit + post)
	h.SnippetHTML = html.EscapeString(oneLine(pre)) + "<mark>" + html.EscapeString(oneLine(hit)) + "</mark>" + html.EscapeString(oneLine(post))
}

func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func trim(s string) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}
