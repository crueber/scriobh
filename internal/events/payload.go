package events

import (
	"regexp"
	"sort"
)

// What an event may carry. Payloads hold ids, types, revisions and counts and
// nothing else: no titles, no names, no manuscript text, no addresses, no file
// names. The rule is enforced twice. The audit test drives every op and checks
// what reached the events table against it, and the stream filters each payload
// through Clean before it leaves the process, so an op added later that
// carelessly puts prose in a payload still cannot send it to a browser.
//
// A value is a bool, a number, a short token (ids, enum words, dotted names:
// letters, digits and _ . : -, up to 64 characters) or a short list of tokens.
// A key must be on the allow-list below.

var payloadKeys = map[string]bool{
	// ids
	"revision_id": true, "new_id": true, "entity_id": true, "to_entity_id": true, "source_id": true, "target_id": true, "change_id": true,
	"compendium_id": true, "target_compendium_id": true, "previous": true, "project_id": true,
	"note_id": true, "tag_id": true, "attachment_id": true, "parent_id": true, "event_id": true,
	// review: ids only, never a comment's text
	"comment_id": true, "thread_id": true, "suggestion_id": true, "share_id": true,
	// counts and revisions
	"revision": true, "words": true, "head_words": true, "tail_words": true, "documents": true, "research_notes": true, "aliases": true,
	"relations": true, "overrides": true, "values": true, "notes": true, "bytes": true, "refs": true, "moved": true, "removed": true,
	"uses": true, "count": true, "events": true,
	// exports: the export's id and its format (md, html, docx, epub, pdf)
	"export_id": true, "format": true,
	// short enums and flags
	"kind": true, "reason": true, "via": true, "layout": true, "primary": true, "content": true, "fields": true, "key": true,
	"status": true, "state": true, "trashed": true, "created": true, "deleted": true, "mode": true,
	"linked": true, "scene": true, "reset": true,
	// AI runs: ids and counts, never a prompt or an answer
	"run_id": true, "input_tokens": true, "output_tokens": true, "cost_micros": true,
}

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,64}$`)

// PayloadKeys lists the allowed keys, sorted, for the tests and the docs.
func PayloadKeys() []string {
	out := make([]string, 0, len(payloadKeys))
	for k := range payloadKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidValue reports whether v is a shape an event payload may carry.
func ValidValue(v any) bool {
	switch x := v.(type) {
	case bool, int, int32, int64, uint, uint32, uint64, float32, float64:
		return true
	case string:
		return tokenRe.MatchString(x)
	case []string:
		if len(x) > 20 {
			return false
		}
		for _, s := range x {
			if !tokenRe.MatchString(s) {
				return false
			}
		}
		return true
	case []any:
		if len(x) > 20 {
			return false
		}
		for _, e := range x {
			if s, ok := e.(string); !ok || !tokenRe.MatchString(s) {
				return false
			}
		}
		return true
	case nil:
		return true
	}
	return false
}

// ValidPayloadKey reports whether k may appear in a payload.
func ValidPayloadKey(k string) bool { return payloadKeys[k] }

// Clean returns the part of a payload that may leave the process: allowed keys
// with allowed values. drop names keys to remove as well (ids that belong to
// the other scope).
func Clean(p map[string]any, drop ...string) map[string]any {
	out := map[string]any{}
	for k, v := range p {
		if !payloadKeys[k] || !ValidValue(v) {
			continue
		}
		skip := false
		for _, d := range drop {
			if d == k {
				skip = true
				break
			}
		}
		if !skip {
			out[k] = v
		}
	}
	return out
}
