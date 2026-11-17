// Package editiondiff compares the text of a book with another text of it: the
// project as it stands against the edition it was imported from (F-373), or one
// stored edition against another (F-377), chapter by chapter, so that a screen
// can step through what changed one difference at a time.
//
// The package is two layers. The core (Compare) takes two lists of chapters as
// plain text, matches them, and diffs the pairs; it knows nothing of the
// database, so its limits are tested with hostile text directly. The ops load
// the two sides (from the project's documents, from the stored baselines, from
// the source files with imports.ReadEpub) and, for the two ops that write,
// turn a difference into a suggestion (edition.suggest) or put a chapter's
// source text back as a revision (edition.take). Nothing here writes a
// document unless the person asks, and every write is an undoable revision.
//
// Matching is a heuristic and says so: every match carries the method that made
// it and a score, and a match made by title or by similarity is labelled as a
// guess. See docs/p7-notes/editiondiff.md.
package editiondiff

// Statuses of a step (one chapter, or the group of chapters that became one).
const (
	StatusUnchanged = "unchanged"
	StatusEdited    = "edited"
	StatusMoved     = "moved"
	StatusSplit     = "split"
	StatusMerged    = "merged"
	StatusAdded     = "added"
	StatusRemoved   = "removed"
)

// Methods a match is made by, from the surest to the weakest.
const (
	// MethodRecorded: the import recorded that this chapter became this document.
	MethodRecorded = "recorded"
	// MethodPath: the document's source path is the chapter's (the import's record, or, between two editions, the
	// same file of the book with a text and a title that agree).
	MethodPath = "path"
	// MethodTitle: the same title (a guess: titles are reused, and "Chapter 3" names a position).
	MethodTitle = "title"
	// MethodSimilarity: the text is mostly the same text (a guess).
	MethodSimilarity = "similarity"
)

// How the text of a step was compared.
const (
	// ComparedFull: paragraph by paragraph, then word by word inside a changed paragraph.
	ComparedFull = "full"
	// ComparedCoarse: a size limit stopped the diff from refining some change, which is shown as a larger one.
	ComparedCoarse = "coarse"
	// ComparedEqual: the two texts are identical; nothing was diffed.
	ComparedEqual = "equal"
	// ComparedNone: nothing to compare (the step is an added or a removed chapter).
	ComparedNone = "none"
	// ComparedNot: the texts differ and were not diffed; the reason is in Note (too large, or the budget of the whole
	// comparison was spent).
	ComparedNot = "not_compared"
	// ComparedSkipped: the two books have so little in common that no chapter was diffed.
	ComparedSkipped = "skipped"
)

// Kinds of a hunk: what the second text did to the first.
const (
	HunkReplace = "replace" // the paragraphs were reworded
	HunkAdded   = "added"   // paragraphs only the second text has
	HunkRemoved = "removed" // paragraphs only the first text has
)

// Ref is one chapter of one side.
type Ref struct {
	Ord        int    `json:"ord,omitempty" desc:"The chapter's place in its edition (1-based); 0 for a document of the project."`
	DocumentID string `json:"document_id,omitempty" desc:"The document, for the project's side."`
	Kind       string `json:"kind,omitempty"`
	Title      string `json:"title"`
	SourcePath string `json:"source_path,omitempty"`
	Words      int    `json:"words"`
}

// Match says how a step's chapters were paired, and how far to trust it.
type Match struct {
	Method string `json:"method" enum:"recorded,path,title,similarity" desc:"What paired the chapters, from the surest: recorded (the import recorded it), path (the same source file), title, similarity (mostly the same text)."`
	// Score is the confidence of the pairing, 0 to 1: 1 for what the import recorded, and for a guess a number made from
	// the method and how much text the two share. It is not a probability.
	Score float64 `json:"score" desc:"Confidence of the pairing, 0 to 1. 1 is what the import recorded; the others are guesses and their scores are only comparable with each other."`
	// Similarity is how much of the text the two chapters share (0 to 1), measured on runs of three words, whatever the method.
	Similarity float64 `json:"similarity" desc:"How much text the paired chapters share, 0 to 1 (runs of three words; sampled on a very large book)."`
	Guess      bool    `json:"guess" desc:"The pairing is a heuristic (title, similarity, or a path between two editions): show it as a guess, not a fact."`
}

// Words are the words of a step.
type Words struct {
	Base int `json:"base" desc:"Words in the first text."`
	Head int `json:"head" desc:"Words in the second text."`
	// Added and Removed are counted the way a revision's diff counts them: words only the second text has, and words
	// only the first has. There is no net figure: a cut is not a loss and a rewrite is not a gain.
	Added   int `json:"added" desc:"Words only the second text has."`
	Removed int `json:"removed" desc:"Words only the first text has."`
}

// Step is one stop in the review: a chapter, or the chapters that became one (merged) or several (split).
type Step struct {
	Index  int    `json:"index" desc:"1-based, in the reading order of the second text; a removed chapter comes after the one before it."`
	Key    string `json:"key" desc:"Names the step for the chapter parameter. Stable while neither text changes."`
	Status string `json:"status" enum:"unchanged,edited,moved,split,merged,added,removed"`
	Base   []Ref  `json:"base" desc:"The chapters of the first text: none for an added step, several for a merged one."`
	Head   []Ref  `json:"head" desc:"The chapters of the second text: none for a removed step, several for a split one."`
	Match  *Match `json:"match,omitempty" desc:"How the chapters were paired; absent for an added or removed step."`
	// Renamed and Moved are flags beside the status, so that a chapter that is edited and moved loses neither fact.
	Renamed  bool   `json:"renamed,omitempty" desc:"The title changed."`
	Moved    bool   `json:"moved,omitempty" desc:"The chapter is somewhere else in the order or the tree (the status is moved only when its text is unchanged)."`
	Words    Words  `json:"words"`
	Compared string `json:"compared" enum:"full,coarse,equal,none,not_compared,skipped"`
	Note     string `json:"note,omitempty" desc:"Why a step was not compared, or compared coarsely."`
	Changes  int    `json:"changes" desc:"How many differences (hunks) there are to review."`
	// Hunks are the differences, for the step named by the chapter parameter only.
	Hunks []Hunk `json:"hunks,omitempty" desc:"Only for the chapter asked for."`
	// Offer says whether a difference of this step can be turned into suggestions or the chapter taken, and when not why.
	Offer *Offer `json:"offer,omitempty" desc:"Only for a comparison with the project: what can be done with this step."`

	baseKeys, headKeys []int // indexes into the two sides
}

// Offer is what can be done with a step of a comparison with the project.
type Offer struct {
	Suggest bool `json:"suggest" desc:"The differences can be offered as suggestions (one chapter, one document, paired one to one)."`
	Take    bool `json:"take" desc:"The chapter can be put back as it was in the edition (edition.take)."`
	// Confirm says taking the chapter needs the person to confirm the pairing, because it is a guess.
	Confirm bool   `json:"confirm,omitempty" desc:"Taking the chapter needs confirm_guess: it was paired with the edition's by a guess, not from the import's record."`
	Reason  string `json:"reason,omitempty" desc:"Why not, when neither is possible."`
}

// Segment is a run of text of a hunk's word diff.
type Segment struct {
	Op   string `json:"op" enum:"equal,insert,delete"`
	Text string `json:"text"`
}

// Hunk is one difference: a run of paragraphs the two texts do not share, to be reviewed on its own.
type Hunk struct {
	ID    string `json:"id" desc:"Names the difference for edition.suggest: a hash of its two texts, so it means the same difference while the texts are as they were."`
	Index int    `json:"index" desc:"1-based, in reading order."`
	Kind  string `json:"kind" enum:"replace,added,removed" desc:"replace: reworded; added: only the second text has it; removed: only the first text has it."`
	// Base and Head are the paragraphs on each side.
	Base     string    `json:"base,omitempty" desc:"The paragraphs of the first text."`
	Head     string    `json:"head,omitempty" desc:"The paragraphs of the second text."`
	Segments []Segment `json:"segments,omitempty" desc:"A replace hunk word by word: delete runs are the first text's, insert runs the second's."`
	Words    Words     `json:"words"`
	// Before is the paragraph just before the difference in the second text, for orientation.
	Before string `json:"before,omitempty"`
	// Offer is empty when the difference can be offered as a suggestion, and says why not otherwise.
	Offer string `json:"offer_refused,omitempty" desc:"Why this difference cannot be offered as a suggestion, when it cannot (a link, a picture or a heading the document would not accept, or a chapter that was split or merged)."`

	// HeadStart and HeadEnd are where the second text's paragraphs are in the second text (bytes); for an
	// insertion point they are equal.
	HeadStart int `json:"-"`
	HeadEnd   int `json:"-"`
}

// Summary totals a comparison.
type Summary struct {
	Steps     int `json:"steps"`
	Unchanged int `json:"unchanged"`
	Edited    int `json:"edited"`
	Moved     int `json:"moved"`
	Split     int `json:"split"`
	Merged    int `json:"merged"`
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	// WordsAdded and WordsRemoved are the sums of the steps': words only the second text has, and words only the first has.
	WordsAdded   int `json:"words_added"`
	WordsRemoved int `json:"words_removed"`
	BaseWords    int `json:"base_words"`
	HeadWords    int `json:"head_words"`
	Changes      int `json:"changes" desc:"Differences to review, all steps."`
	// Uncounted is how many steps differ but were not diffed (too large, or the comparison's budget was spent), so
	// their words are not in the totals.
	Uncounted int `json:"uncounted,omitempty"`
}

// Side names what one text is.
type Side struct {
	Kind      string `json:"kind" enum:"edition,project"`
	ProjectID string `json:"project_id"`
	Project   string `json:"project,omitempty" desc:"The project's name."`
	EditionID string `json:"edition_id,omitempty"`
	Label     string `json:"label,omitempty"`
	SHA256    string `json:"sha256,omitempty" desc:"Of the edition's source file."`
	Chapters  int    `json:"chapters"`
	Words     int    `json:"words"`
	// Source says where an edition's text was read from: the source file (read again, as an import would), or the
	// text the import recorded (when the file is not kept).
	Source string `json:"source,omitempty" enum:"file,record,project"`
}

// Result is a comparison.
type Result struct {
	Mode    string  `json:"mode" enum:"project,editions" desc:"project: the project as it stands against an edition; editions: two editions against each other."`
	Base    Side    `json:"base" desc:"The first text: the edition the comparison is against."`
	Head    Side    `json:"head" desc:"The second text: the project as it stands, or the later edition."`
	Steps   []Step  `json:"steps"`
	Summary Summary `json:"summary"`
	// TooDifferent says the two texts share so little that no chapter was diffed: showing every paragraph as changed
	// would be noise. The steps are still paired where a pairing was recorded.
	TooDifferent bool `json:"too_different" desc:"The two texts have too little in common to compare chapter by chapter; no chapter was diffed."`
	// Partial says the budget of the whole comparison was spent: later steps were not diffed (see their note).
	Partial bool     `json:"partial" desc:"The comparison's work budget was spent; some steps were not diffed."`
	Notices []string `json:"notices,omitempty"`
}

// Unit is one chapter of one side as the core takes it.
type Unit struct {
	// Key names the unit within its side (the document id, or e<ord>).
	Key string
	// ID is the document id for the project's side.
	ID string
	// Mapped is, for an edition's chapter in a comparison with the project, the document the import recorded it
	// became. Empty otherwise.
	Mapped string
	Path   string
	Kind   string
	Title  string
	Ord    int
	Parent int // index of the parent in the same list; -1 for the top
	Text   string
}
