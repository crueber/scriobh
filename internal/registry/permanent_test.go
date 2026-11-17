package registry

import (
	"sort"
	"strings"
	"testing"
)

// reversible is every op that deletes, removes, merges, clears, rejects or revokes
// something and is not Permanent, with the reason it is not: what it takes away
// goes to the trash or the history, is metadata that is kept elsewhere, or can be
// put back with another op. An op that destroys data beyond the trash sets
// Op.Permanent instead, and the MCP endpoint then refuses it by default.
//
// A new op with a destructive name (or a DELETE route) fails the test below until
// someone has decided which it is.
var reversible = map[string]string{
	"ai.accept.reject": "a held suggestion is dismissed; the text was never touched", "ai.extract.dismiss": "a proposed entity is dismissed; nothing existed yet",
	"ai.lab.accept_all": "writes the text through the save seam, which keeps a history entry", "ai.lab.dismiss": "marks are dismissed, and show again if asked for",
	"ai.review.note.dismiss": "a note is marked dismissed, not deleted", "alternative.delete": "resolves an alternative; the other wordings stay in the history",
	"compendium.member.remove": "a person is taken off and can be added again; what they wrote stays",
	"desktop.idp.cancel":       "a switch of identity provider that waits for a restart is dropped and can be asked for again; nothing was changed yet",
	"compendium.ref.remove":    "a reference is removed and can be added again; overrides stay dormant",
	"kindle.address.delete":    "a stored Send-to-Kindle address is removed and can be entered again; nothing else is touched",
	"studio.card.revoke":       "a progress card's link stops working; the card shows only numbers and a new one can be made at once",
	"key.revoke":               "access, not data: a key is revoked and another can be made", "project.compendium.ref.remove": "a reference is removed and can be added again; overrides stay dormant",
	"project.passphrase.clear":   "access, not data: the lock is removed and can be set again; no text is touched",
	"project.entity.link.remove": "a link is removed and can be added again; the override is kept, dormant", "project.member.remove": "a person is taken off and can be added again; what they wrote stays",
	"review.resolve_all": "threads are resolved and can be reopened", "session.revoke": "access, not data: a session is ended", "session.revoke_others": "access, not data: sessions are ended",
	"share.revoke": "access, not data: a link is revoked and another can be made", "suggestion.accept_all": "writes the text through the save seam, which keeps a history entry",
	"suggestion.reject": "a suggestion is marked rejected, not deleted", "timeline.event.delete": "moves the event to the trash; timeline.event.restore brings it back",
	"timeline.scene.unlink": "a link is removed and can be made again",
}

var destructiveWords = map[string]bool{
	"delete": true, "remove": true, "purge": true, "prune": true, "merge": true, "clear": true, "discard": true, "unlink": true,
	"revoke": true, "revoke_others": true, "reject": true, "dismiss": true, "resolve_all": true, "accept_all": true,
}

func TestEveryDestructiveOpIsDecided(t *testing.T) {
	r := Build(nil, nil, "")
	var undecided, both []string
	seen := map[string]bool{}
	permanent := 0
	for _, e := range r.Entries() {
		n := e.Op.Name
		last := n[strings.LastIndex(n, ".")+1:]
		destructive := e.Op.Method == "DELETE" || destructiveWords[last]
		why, isRev := reversible[n]
		if isRev {
			seen[n] = true
		}
		if e.Op.Permanent {
			permanent++
		}
		switch {
		case e.Op.Permanent && isRev:
			both = append(both, n)
		case destructive && !e.Op.Permanent && !isRev:
			undecided = append(undecided, n)
		case isRev && why == "":
			t.Errorf("%s is listed as reversible without a reason", n)
		}
	}
	sort.Strings(undecided)
	if len(undecided) > 0 {
		t.Errorf("these ops delete, remove, merge or clear and have no decision: set Op.Permanent or add them to the reversible table with the reason:\n%s", strings.Join(undecided, "\n"))
	}
	if len(both) > 0 {
		t.Errorf("both Permanent and listed reversible: %v", both)
	}
	for n := range reversible {
		if !seen[n] {
			t.Errorf("%s is in the reversible table but is not an op", n)
		}
	}
	if permanent < 25 {
		t.Errorf("only %d permanent ops", permanent)
	}
}

// The ops that destroy a whole project or Compendium, and the ones that destroy
// the history, are marked.
func TestKnownPermanentOps(t *testing.T) {
	r := Build(nil, nil, "")
	for _, n := range []string{"project.purge", "compendium.purge", "stash.delete", "revision.delete", "revision.prune", "comment.delete", "entity.merge",
		"entity.field.remove", "project.attachment.delete", "compendium.attachment.delete", "project.tag.merge", "compendium.tag.delete", "webhook.delete"} {
		e, ok := r.Lookup(n)
		if !ok || !e.Op.Permanent {
			t.Errorf("%s must be Permanent", n)
		}
	}
	for _, n := range []string{"doc.trash", "project.trash", "entity.trash", "stash.trash", "doc.update", "doc.restore"} {
		if e, ok := r.Lookup(n); !ok || e.Op.Permanent {
			t.Errorf("%s goes to the trash or the history and must not be Permanent", n)
		}
	}
}
