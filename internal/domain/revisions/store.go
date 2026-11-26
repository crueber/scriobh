// Package revisions is a document's append-only history: automatic versions
// while editing, named snapshots, the version a restore leaves behind, pruning,
// and diffs between versions.
//
// It is a leaf package. docs imports it, so a save can write its revision in the
// same transaction as the document, and the ops here never write a document
// body themselves; revision.restore lives in docs for that reason.
//
// # What an automatic version is
//
// A revision stores the text a document had, not the text a save is about to
// write. When a save that changes the body arrives, CaptureHead looks at the
// head as it stands: if that exact text is not already the newest revision and
// the newest revision was written at least AutosaveInterval ago, the head is
// recorded, stamped with the time it became the head and the person who saved
// it. Two consequences follow, both on purpose:
//
//   - The end of an editing session is kept. Today's first save records
//     yesterday's last text, stamped yesterday, instead of losing it.
//   - "Active editing" needs no idle timer. The clock is the time since the last
//     revision was written, read from the injectable db clock; a save after a
//     long pause always qualifies, and a burst of saves writes at most one
//     revision per interval.
//
// The newest saved text is the document itself, so history lists it as the
// current version rather than storing a copy nobody asked for.
//
// # Storage
//
// Content is kept once per (document, content hash) in revision_blobs and
// referenced by revision rows. Content above CompressAbove bytes is gzip
// compressed. Blobs belong to their document, so a purge removes them.
package revisions

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Reasons a revision is written.
const (
	ReasonAutosave    = "autosave"
	ReasonManual      = "manual"
	ReasonSnapshot    = "snapshot" // taken by the system before a destructive edit (split, restore)
	ReasonRestore     = "restore"
	ReasonImport      = "import"
	ReasonAlternative = "alternative"
	// ReasonSuggestionAccepted is a collaborator's suggestion an editor accepted.
	ReasonSuggestionAccepted = "suggestion-accepted"
	// ReasonCut is the text before a save that removed a big part of the chapter (see IsBigCut).
	ReasonCut = "cut"
	// ReasonSplit is the whole text before a split (doc.split).
	ReasonSplit = "split"
	// ReasonJoin is the text before two documents are joined.
	ReasonJoin = "join"
)

var reasons = []string{ReasonAutosave, ReasonManual, ReasonSnapshot, ReasonRestore, ReasonImport, "ai-accepted", ReasonAlternative, ReasonSuggestionAccepted,
	ReasonCut, ReasonSplit, ReasonJoin}

// Tunables.
const (
	// AutosaveInterval is the least time between two automatic revisions of one
	// document, measured between the times the rows were written.
	AutosaveInterval = 5 * time.Minute
	// MaxCutVersions caps the unlabelled cut versions one document keeps. A big
	// cut is not limited by AutosaveInterval, so a burst of them is bounded here:
	// the oldest beyond the cap go, at each cut and at each prune. A labelled
	// version is kept forever like any other and is not counted.
	MaxCutVersions = 10
	// CompressAbove is the content size above which a blob is gzip-compressed.
	CompressAbove = 8 << 10
	// MaxContent bounds what a blob may decompress to (the document body cap).
	MaxContent = 2 << 20

	maxLabel = 120
	maxNote  = 2000
)

// Hash is the content hash revisions and documents share: sha256, hex.
func Hash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

var emptyHash = Hash("")

// RecordIn describes a revision to write.
type RecordIn struct {
	DocumentID string
	Revision   int    // documents.revision the content had
	Content    string // the Markdown
	Words      int
	AuthorID   string // "" for none
	Reason     string
	Label      string
	Note       string
	// At is when the content became the document's head. Empty means now.
	At string
	// Now is when the row is written. Empty means now.
	Now string
}

// Record stores the content (once per document and hash) and appends a
// revision row, inside the caller's transaction. It returns the revision id.
func Record(tx *sql.Tx, in RecordIn) (string, error) {
	if !oneOf(reasons, in.Reason) {
		return "", fmt.Errorf("revisions: unknown reason %q", in.Reason)
	}
	now := in.Now
	if now == "" {
		now = db.Now()
	}
	at := in.At
	if at == "" {
		at = now
	}
	hash := Hash(in.Content)
	var have int
	if err := tx.QueryRow(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, in.DocumentID, hash).Scan(&have); err != nil {
		return "", err
	}
	if have == 0 {
		enc, blob, err := encode(in.Content)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(`INSERT INTO revision_blobs(document_id, content_hash, encoding, content, raw_size) VALUES (?,?,?,?,?)`,
			in.DocumentID, hash, enc, blob, len(in.Content)); err != nil {
			return "", err
		}
	}
	// The author is looked up, so a person who no longer exists leaves the
	// revision without an author instead of failing the save that wrote it.
	id := db.NewID("rev")
	_, err := tx.Exec(`INSERT INTO document_revisions(id, document_id, revision, content_hash, word_count, author_id, reason, label, note, created_at, recorded_at)
		VALUES (?,?,?,?,?,(SELECT id FROM users WHERE id = ?),?,?,?,?,?)`,
		id, in.DocumentID, in.Revision, hash, in.Words, in.AuthorID, in.Reason, in.Label, in.Note, at, now)
	return id, err
}

// encode compresses content above CompressAbove when that makes it smaller.
func encode(content string) (encoding string, blob []byte, err error) {
	if len(content) <= CompressAbove {
		return "identity", []byte(content), nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		return "", nil, err
	}
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	if buf.Len() >= len(content) {
		return "identity", []byte(content), nil
	}
	return "gzip", buf.Bytes(), nil
}

// decode reverses encode and checks the bytes against the hash they were
// stored under.
func decode(encoding string, blob []byte, hash string) (string, error) {
	var raw []byte
	switch encoding {
	case "identity":
		raw = blob
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(blob))
		if err != nil {
			return "", err
		}
		raw, err = io.ReadAll(io.LimitReader(zr, MaxContent+1))
		if err != nil {
			return "", err
		}
		if len(raw) > MaxContent {
			return "", errors.New("revisions: stored content exceeds the document limit")
		}
	default:
		return "", fmt.Errorf("revisions: unknown encoding %q", encoding)
	}
	s := string(raw)
	if Hash(s) != hash {
		return "", errors.New("revisions: stored content does not match its hash")
	}
	return s, nil
}

// querier is the read half of *sql.DB, *sql.Tx and *sql.Conn.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// Content returns the text of one revision of a document. A revision of
// another document is not found.
func Content(q querier, documentID, revisionID string) (Revision, string, error) {
	var (
		r         Revision
		enc, hash string
		blob      []byte
		author    sql.NullString
	)
	err := q.QueryRow(`SELECT r.id, r.document_id, r.revision, r.reason, r.label, r.note, r.word_count, r.author_id, r.created_at, r.recorded_at,
			r.content_hash, b.encoding, b.content, b.raw_size
		FROM document_revisions r JOIN revision_blobs b ON b.document_id = r.document_id AND b.content_hash = r.content_hash
		WHERE r.id = ? AND r.document_id = ?`, revisionID, documentID).
		Scan(&r.ID, &r.DocumentID, &r.Revision, &r.Reason, &r.Label, &r.Note, &r.WordCount, &author, &r.CreatedAt, &r.RecordedAt,
			&hash, &enc, &blob, &r.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, "", ops.NotFound("revision")
	}
	if err != nil {
		return Revision{}, "", err
	}
	r.AuthorID = author.String
	r.ContentHash = hash
	r.Pinned = pinned(r.Reason, r.Label)
	content, err := decode(enc, blob, hash)
	if err != nil {
		return Revision{}, "", ops.Internal(err)
	}
	return r, content, nil
}

// pinned says whether pruning must keep a revision forever: a named snapshot,
// or any version a person labelled.
func pinned(reason, label string) bool { return reason == ReasonManual || label != "" }

// HeadState is a document as it stood before a write replaced it. The writer
// already has these values from the row it locked, so revisions never re-read
// the document.
type HeadState struct {
	DocumentID string
	Content    string
	Hash       string // Hash(Content); empty means compute it
	Revision   int    // documents.revision before the write
	Words      int
	ContentAt  string // documents.content_at; may be empty
	UpdatedAt  string // documents.updated_at, the fallback stamp
	By         string // documents.content_by; may be empty
}

func (h HeadState) hash() string {
	if h.Hash != "" {
		return h.Hash
	}
	return Hash(h.Content)
}

// stamp is when the text became the head.
func (h HeadState) stamp() string {
	if h.ContentAt != "" {
		return h.ContentAt
	}
	return h.UpdatedAt
}

// latest is the newest revision row by the time it was written.
func latest(tx *sql.Tx, docID string) (hash, recordedAt string, ok bool, err error) {
	err = tx.QueryRow(`SELECT content_hash, recorded_at FROM document_revisions WHERE document_id = ? ORDER BY recorded_at DESC, id DESC LIMIT 1`, docID).
		Scan(&hash, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return hash, recordedAt, err == nil, err
}

// CaptureHead is called by a save that changes the body, inside its
// transaction, with the document as it stood before the save. It records that
// state as an automatic revision when the rules in the package comment say so,
// and reports whether it did.
func CaptureHead(tx *sql.Tx, h HeadState, now string) (bool, error) {
	hash := h.hash()
	lastHash, lastAt, have, err := latest(tx, h.DocumentID)
	if err != nil {
		return false, err
	}
	switch {
	case have && lastHash == hash:
		return false, nil // the head is already the newest revision
	case have:
		if t, err := db.Parse(lastAt); err == nil {
			if n, err := db.Parse(now); err == nil && n.Sub(t) < AutosaveInterval {
				return false, nil // still inside the throttle window
			}
		}
	case hash == emptyHash:
		return false, nil // a document nobody has written in has no history worth keeping
	}
	_, err = Record(tx, RecordIn{DocumentID: h.DocumentID, Revision: h.Revision, Content: h.Content, Words: h.Words, AuthorID: h.By,
		Reason: ReasonAutosave, At: h.stamp(), Now: now})
	return err == nil, err
}

// EnsureRecorded preserves the head before something replaces it (a restore, a
// split): when no revision of the document holds the head's exact text, one is
// written with the given reason and a note saying why, ignoring the autosave
// throttle. The explanation goes in the note, never the label: a label pins a
// revision forever, and these copies are safety nets that retention may thin.
func EnsureRecorded(tx *sql.Tx, h HeadState, reason, note, now string) (bool, error) {
	hash := h.hash()
	if hash == emptyHash {
		return false, nil // nothing to preserve
	}
	var have int
	if err := tx.QueryRow(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, h.DocumentID, hash).Scan(&have); err != nil {
		return false, err
	}
	if have > 0 {
		return false, nil
	}
	_, err := Record(tx, RecordIn{DocumentID: h.DocumentID, Revision: h.Revision, Content: h.Content, Words: h.Words, AuthorID: h.By,
		Reason: reason, Note: note, At: h.stamp(), Now: now})
	return err == nil, err
}

// CaptureBefore keeps the head as one version with the given reason, whatever
// the autosave interval says. It is for a moment that is worth its own labelled
// version: a split, a join, or a save that is a big cut (IsBigCut). It reports
// whether it wrote one. Nothing is written when the head is empty, or when the
// newest version already holds exactly the head's text (a snapshot taken in the
// same save, say): one version per moment, never two of the same text. A cut
// also trims the unlabelled cut versions to MaxCutVersions.
func CaptureBefore(tx *sql.Tx, h HeadState, reason, now string) (bool, error) {
	hash := h.hash()
	if hash == emptyHash {
		return false, nil
	}
	lastHash, _, have, err := latest(tx, h.DocumentID)
	if err != nil {
		return false, err
	}
	if have && lastHash == hash {
		return false, nil
	}
	if _, err := Record(tx, RecordIn{DocumentID: h.DocumentID, Revision: h.Revision, Content: h.Content, Words: h.Words, AuthorID: h.By,
		Reason: reason, At: h.stamp(), Now: now}); err != nil {
		return false, err
	}
	if reason == ReasonCut {
		if err := trimCuts(tx, h.DocumentID); err != nil {
			return true, err
		}
	}
	return true, nil
}

// IsBigCut says whether a save that takes a chapter from before words to after
// words is a big cut: it removes more than CutWords words, or more than a fifth
// of the words the chapter had. Growth is never a cut. The counts are the app's
// word count (text.WordCount), which does not count ghosted text, so a ghosted
// passage counts as removed, as it does on the page.
func IsBigCut(before, after int) bool {
	removed := before - after
	if removed <= 0 {
		return false
	}
	return removed > CutWords || removed*cutFifth > before
}

// The thresholds IsBigCut applies.
const (
	CutWords = 200
	cutFifth = 5
)

func oneOf(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
