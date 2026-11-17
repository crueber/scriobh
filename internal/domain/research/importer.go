package research

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/text"
)

// What an import may write into a project's research. The importer reads a
// foreign project into notes of its own and hands them over here; this is the
// only place that writes a research note outside the research ops, so the
// limits, the search text and the tags are the ops' own.

// Limits an imported note is cleaned to (the ops' own).
const (
	MaxTitleChars = maxTitle
	MaxBodyBytes  = maxBody
	MaxTagCount   = maxTags
	MaxTagChars   = maxTagLen
	MaxFolderDeep = maxDepth
)

// Imported is one note, or folder of notes, to create.
type Imported struct {
	// Parent is the index of the folder it is in (an earlier entry), or -1.
	Parent    int
	Kind      string // note, clipping or folder
	Title     string
	Body      string
	SourceURL string
	Tags      []string
	ID        string // set by InsertImported
}

// Clean brings an imported note to the limits of the research ops. It returns a
// note of at most one megabyte of valid text, a title of one to 300 characters,
// at most 20 tags of at most 40 characters, and an http or https source address
// or none. It says in what it returns what it had to cut ("" when nothing).
func (n *Imported) Clean() (cut string) {
	if !oneOf(Kinds, n.Kind) {
		n.Kind = "note"
	}
	title := cleanLine(strings.ToValidUTF8(n.Title, ""))
	if title == "" {
		title = "Untitled"
	}
	if rs := []rune(title); len(rs) > maxTitle {
		title, cut = strings.TrimSpace(string(rs[:maxTitle])), "title cut"
	}
	n.Title = title
	if n.Kind == "folder" {
		n.Body, n.SourceURL = "", ""
	} else {
		b := strings.ReplaceAll(strings.ToValidUTF8(n.Body, ""), "\x00", "")
		if len(b) > maxBody {
			b = cutBody(b, maxBody)
			cut = "text cut at 1 MB"
		}
		n.Body = b
		if s, err := cleanSource(n.SourceURL); err == nil {
			n.SourceURL = s
		} else {
			n.SourceURL = ""
		}
	}
	var tags []string
	seen := map[string]bool{}
	for _, t := range n.Tags {
		t = cleanLine(strings.ToValidUTF8(t, ""))
		if rs := []rune(t); len(rs) > maxTagLen {
			t = strings.TrimSpace(string(rs[:maxTagLen]))
		}
		if t == "" || seen[text.FoldTitle(t)] {
			continue
		}
		seen[text.FoldTitle(t)] = true
		if len(tags) >= maxTags {
			cut = "tags cut at 20"
			break
		}
		tags = append(tags, t)
	}
	n.Tags = tags
	return cut
}

// InsertImported writes the notes (already cleaned) as the project's research,
// at the end of the top level, in the caller's transaction, and sets each one's
// ID. A note's parent must come before it. It writes the search text and the tags
// and emits nothing: the importer says what it did once, for the whole batch.
func InsertImported(tx *sql.Tx, projectID, userID, now string, notes []Imported) error {
	o := owner{OwnerProject, projectID}
	var last float64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position), 0) FROM research_notes WHERE project_id = ? AND parent_id IS NULL`, projectID).Scan(&last); err != nil {
		return err
	}
	count := map[int]int{}
	for i := range notes {
		n := &notes[i]
		if utf8.RuneCountInString(n.Title) > maxTitle || len(n.Body) > maxBody {
			return fmt.Errorf("research note %d was not cleaned", i)
		}
		var parent any
		if n.Parent >= 0 {
			if n.Parent >= i || notes[n.Parent].Kind != "folder" {
				return fmt.Errorf("research note %d has a parent that is not an earlier folder", i)
			}
			parent = notes[n.Parent].ID
		}
		count[n.Parent]++
		pos := float64(count[n.Parent]) * db.PositionGap
		if n.Parent < 0 {
			pos += last
		}
		n.ID = db.NewID("rsn")
		words := text.WordCount(n.Body)
		if _, err := tx.Exec(`INSERT INTO research_notes(id, project_id, compendium_id, parent_id, kind, title, body, source_url, retrieved_on, quote, status,
			position, revision, word_count, content_hash, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.ID, projectID, nil, parent, n.Kind, n.Title, n.Body, n.SourceURL, "", "", "inbox", pos, 1, words, hashContent(n.Body, ""),
			nullIfEmpty(userID), now, now); err != nil {
			return err
		}
		if err := writeText(tx, o, n.ID, n.Title, n.SourceURL, n.Body, ""); err != nil {
			return err
		}
		if len(n.Tags) > 0 {
			if _, err := setTags(tx, o, n.ID, n.Tags, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// cutBody cuts text to at most max bytes without breaking what an import wrote.
// A private note is one pair of %% inside one line, so a cut in the last line
// takes the whole of that line from its first %% on: an opener left without its
// closer would hide, or in some readers show, what follows it.
func cutBody(b string, max int) string {
	b = strings.ToValidUTF8(b[:max], "")
	nl := strings.LastIndexByte(b, '\n')
	if i := strings.Index(b[nl+1:], "%%"); i >= 0 {
		b = b[:nl+1+i]
	}
	return b
}
