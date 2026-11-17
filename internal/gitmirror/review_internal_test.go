package gitmirror

import "testing"

// A push is built from the mirror's manuscript and notes folders and nothing else. Review data (comments,
// suggestions, share links, the revision history) has no folder in the mirror, and no such path is
// pushable even if one were ever written.
func TestReviewDataIsNeverPushable(t *testing.T) {
	for _, p := range []string{"comments/c.md", "comments/thread.json", "review/queue.md", "suggestions/s.md", "shares/links.json", "revisions/r.md", "stash/s.md",
		"manuscript/comments.json", "manuscript/.comments/c.md", "notifications/n.md", "manuscript/../comments/c.md", "comments.md", ".scriobh/comments.md"} {
		if pushable(p) {
			t.Errorf("%s is pushable", p)
		}
	}
	for _, p := range []string{"manuscript/01-chapter.md", "notes/n.md"} {
		if !pushable(p) {
			t.Errorf("%s should still be pushable", p)
		}
	}
}
