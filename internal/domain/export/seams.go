package export

// The seams the compile pipeline (internal/domain/compile) builds on: the
// project and its documents as one snapshot in reading order, the file
// description with its checksum, and the manifest row every export leaves.
// Keeping them here keeps one definition of the walk order, the caps and the
// record.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// dropNotCompiled removes the documents marked "do not compile", and everything
// inside one: leaving a chapter out of a book leaves its scenes out too. It
// returns the ids of what it removed, in the order the documents were read.
// With keep set nothing is removed.
func dropNotCompiled(all []*doc, keep bool) ([]*doc, []string) {
	if keep {
		return all, nil
	}
	byID := make(map[string]*doc, len(all))
	for _, x := range all {
		byID[x.ID] = x
	}
	state := map[string]int8{} // 1 compiled, -1 excluded
	var excluded func(x *doc, depth int) bool
	excluded = func(x *doc, depth int) bool {
		if s, ok := state[x.ID]; ok {
			return s < 0
		}
		state[x.ID] = 1 // a parent cycle cannot recurse
		ex := !x.Include
		if !ex && depth < 10_000 {
			if p, ok := byID[x.ParentID]; ok && p != x {
				ex = excluded(p, depth+1)
			}
		}
		if ex {
			state[x.ID] = -1
		}
		return ex
	}
	var kept []*doc
	var skipped []string
	for _, x := range all {
		if excluded(x, 0) {
			skipped = append(skipped, x.ID)
		} else {
			kept = append(kept, x)
		}
	}
	if len(skipped) == 0 {
		return all, nil
	}
	return kept, skipped
}

// withSkipped adds the documents that were left out to a manifest or an audit
// detail: their ids in a manifest, only their number in an audit entry.
func withSkipped(m map[string]any, skipped []string, ids bool) map[string]any {
	if len(skipped) == 0 {
		return m
	}
	if ids {
		m["not_compiled"] = skipped
	} else {
		m["skipped"] = len(skipped)
	}
	return m
}

// SourceDoc is one live document of a project, in reading order.
type SourceDoc struct {
	ID, ParentID, Kind, Title, Content, Status string
	SynopsisShort, SynopsisLong                string
	Revision, WordCount, Depth                 int
	// Include is the document's own include_in_compile flag.
	Include bool
	// Note is set for a document of kind note and for everything inside one.
	Note bool
	// Hash is the SHA-256 of Content as stored.
	Hash string
}

// SourceProject is the project row a compile needs.
type SourceProject struct {
	ID, Title, Subtitle, Author, Kind, Language, Genre, UpdatedAt, CoverColor string
}

// Source is a project as one read snapshot: the manuscript in reading order,
// then the documents of kind note (and what is inside them) in reading order.
// Documents of kind research and Compendium documents are not in it.
type Source struct {
	Project SourceProject
	Docs    []SourceDoc
}

// LoadSource reads a project for a compile, under the same caps as an export.
func LoadSource(ctx context.Context, d *db.DB, projectID string) (*Source, error) {
	p, all, err := load(ctx, d, projectID)
	if err != nil {
		return nil, err
	}
	manuscript, notes := tree(all)
	s := &Source{Project: SourceProject{ID: p.ID, Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind,
		Language: p.Language, Genre: p.Genre, UpdatedAt: p.UpdatedAt, CoverColor: p.CoverColor}}
	ids := map[string]*doc{}
	for _, x := range manuscript {
		ids[x.ID] = x
	}
	for _, x := range notes {
		ids[x.ID] = x
	}
	depthOf := map[string]int{}
	var depth func(x *doc, guard int) int
	depth = func(x *doc, guard int) int {
		if v, ok := depthOf[x.ID]; ok {
			return v
		}
		depthOf[x.ID] = 0
		v := 0
		if p, ok := ids[x.ParentID]; ok && p != x && guard < 10_000 {
			v = depth(p, guard+1) + 1
		}
		depthOf[x.ID] = v
		return v
	}
	add := func(x *doc, isNote bool) {
		s.Docs = append(s.Docs, SourceDoc{ID: x.ID, ParentID: x.ParentID, Kind: x.Kind, Title: x.Title, Content: x.Content, Status: x.Status,
			SynopsisShort: x.SynopsisShort, SynopsisLong: x.SynopsisLong, Revision: x.Revision, WordCount: x.WordCount,
			Depth: depth(x, 0), Include: x.Include, Note: isNote, Hash: x.hash})
	}
	for _, x := range manuscript {
		add(x, false)
	}
	for _, x := range notes {
		add(x, true)
	}
	return s, nil
}

// Held is a document an export held, with the hash of its text as exported.
type Held struct {
	ID     string
	SHA256 string
}

// HashOf is the SHA-256 of a text, hex.
func HashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// NewFile describes a built export: a fresh export id, the name, the type, the
// size and checksum of body.
func NewFile(name, contentType string, body []byte, documents int) File {
	return newFile(name, contentType, body, documents)
}

// Record writes an export's row, with the ids and hashes of the documents it
// held, and its audit entry, in one transaction. manifest adds keys to the
// manifest JSON, audit to the audit detail. Neither may hold manuscript text or
// personal details: ids, counts, hashes and option names only.
func Record(c *ops.Ctx, projectID, preset, format string, file File, held []Held, manifest, audit map[string]any) error {
	docs := make([]*doc, 0, len(held))
	for _, h := range held {
		docs = append(docs, &doc{ID: h.ID, hash: h.SHA256})
	}
	return record(c, projectID, preset, format, file, docs, manifest, audit)
}
