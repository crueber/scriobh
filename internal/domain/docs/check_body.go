package docs

import "errors"

var errNoEdits = errors.New("no edits, or too many")

// CheckEditsOnBody says whether edits (ranges in UTF-16 code units of body,
// each with the text it expects to find) could be applied to a document whose
// text is body and which holds no alternatives: the same range, hidden-text,
// text-safety and structure checks an accepted suggestion passes
// (CheckReviewEdits), without a database. The importer uses it to decide, from
// the text alone, which tracked changes of a Word file can become suggestions.
func CheckEditsOnBody(body string, edits []AIEdit) error {
	if len(edits) == 0 || len(edits) > MaxAIEdits {
		return errNoEdits
	}
	s := &altSession{body: body}
	_, _, err := planEdits(s, edits, "a suggestion")
	return err
}
