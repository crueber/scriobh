package gitmirror

import "testing"

// A push carries exactly the Markdown the manifest lists under manuscript/ and
// notes/. The stash (a stash/ folder in the mirror) is never among it, nor
// research, nor anything else.
func TestStashIsNeverPushable(t *testing.T) {
	for _, p := range []string{"stash/a-cut-scene--abc123.md", "stash/x.md", "research/x.md", "project.json", "manuscript/../stash/x.md"} {
		if pushable(p) {
			t.Errorf("%s must not be pushable", p)
		}
	}
	for _, p := range []string{"manuscript/01-chapter.md", "notes/02-note.md"} {
		if !pushable(p) {
			t.Errorf("%s must be pushable", p)
		}
	}
}
