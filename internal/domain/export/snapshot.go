package export

import (
	"context"

	"github.com/crueber/scriobh/internal/db"
)

// Snapshot is a project laid out the way the Markdown bundle lays it out: the
// same walk, the same numbered file names, the same slugs. The mirror writes
// it to disk; it is built from one read transaction, so the project row and
// every document are one consistent state.
type Snapshot struct {
	Project SnapshotProject
	// Files are the documents in layout order: the manuscript, then the notes.
	Files []SnapshotFile
	// Editions are the books the project was imported from: metadata only.
	Editions []EditionMeta
}

// SnapshotProject is the project row the mirror's manifest needs.
type SnapshotProject struct {
	ID, Slug, Title, Subtitle, Author, Kind, Language, Genre, Status string
	Trashed, Archived                                                bool
}

// SnapshotFile is one document and where it goes, relative to the project's
// directory (manuscript/... or notes/...). Content is the stored text, byte
// for byte.
type SnapshotFile struct {
	Path      string
	Content   []byte
	SHA256    string
	DocID     string
	Kind      string
	Title     string
	Status    string
	WordCount int
	// Outline metadata: the one-line and long synopsis, scene metadata, the
	// word target, and whether the document is compiled.
	SynopsisShort, SynopsisLong, Pov, Location, StoryDate, StoryDateEnd string
	TargetWords                                                         int
	IncludeInCompile                                                    bool
	// The entities the point of view and location are linked to, with the
	// book's names for them; nil when none is linked or it is out of scope.
	PovEntity, LocationEntity *EntityRef
}

// LoadSnapshot reads a project for the mirror. Unlike an export it has no size
// caps: a large project is still mirrored. Documents of kind research and
// Compendium documents are left out, as in an export. It returns a not-found
// error when the project does not exist.
func LoadSnapshot(ctx context.Context, d *db.DB, projectID string, includeNotes bool) (*Snapshot, error) {
	p, all, err := loadWith(ctx, d, projectID, false)
	if err != nil {
		return nil, err
	}
	manuscript, notes := tree(all)
	if !includeNotes {
		notes = nil
	}
	entries, _ := layout(manuscript, notes)
	s := &Snapshot{
		Project: SnapshotProject{ID: p.ID, Slug: p.Slug, Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind,
			Language: p.Language, Genre: p.Genre, Status: p.Status, Trashed: p.Trashed, Archived: p.Archived},
		Files:    make([]SnapshotFile, 0, len(entries)),
		Editions: p.Editions,
	}
	for _, e := range entries {
		s.Files = append(s.Files, SnapshotFile{Path: e.path, Content: e.data, SHA256: e.doc.hash, DocID: e.doc.ID,
			Kind: e.doc.Kind, Title: e.doc.Title, Status: e.doc.Status, WordCount: e.doc.WordCount,
			SynopsisShort: e.doc.SynopsisShort, SynopsisLong: e.doc.SynopsisLong, Pov: e.doc.Pov, Location: e.doc.Location,
			StoryDate: e.doc.StoryDate, StoryDateEnd: e.doc.StoryDateEnd, TargetWords: e.doc.TargetWords, IncludeInCompile: e.doc.Include,
			PovEntity: e.doc.PovEntity, LocationEntity: e.doc.LocationEntity})
	}
	return s, nil
}
