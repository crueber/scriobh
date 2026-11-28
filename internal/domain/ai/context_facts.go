package ai

import (
	"fmt"
	"strings"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/timeline"
)

// maxSummaries bounds the earlier-chapter summaries one request carries.
const maxSummaries = 80

// earlierSummaries lists the synopsis of every earlier chapter or scene, in
// reading order, each under the id a finding may cite ("[doc:ID]"). A document
// flagged never-send, or with no synopsis, is left out.
func (l *loader) earlierSummaries(targetID string) (string, error) {
	if t, _ := l.loadDoc(targetID, "document_id", false); t == nil {
		return "", nil
	}
	if err := l.loadTree(); err != nil {
		return "", err
	}
	rows, err := l.d.Read().QueryContext(l.ctx, `SELECT id, synopsis_long FROM documents WHERE project_id = ? AND deleted_at IS NULL AND synopsis_long <> ''`, l.projectID)
	if err != nil {
		return "", err
	}
	long := map[string]string{}
	for rows.Next() {
		var id, s string
		if err := rows.Scan(&id, &s); err != nil {
			rows.Close()
			return "", err
		}
		long[id] = s
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	byID := map[string]docref.Node{}
	for _, n := range l.nodes {
		byID[n.ID] = n
	}
	var b strings.Builder
	n := 0
	for _, id := range docref.Order(l.nodes) {
		if id == targetID {
			break
		}
		nd := byID[id]
		if nd.Kind != "chapter" && nd.Kind != "scene" {
			continue
		}
		if l.nodeEx[id] {
			l.skip(lib.ChipSummaries, id, "never_send", "An earlier chapter is marked never-send, so its summary is not included.")
			continue
		}
		s := strings.TrimSpace(long[id])
		if s == "" {
			s = strings.TrimSpace(l.nodeSyn[id])
		}
		if s == "" {
			continue
		}
		if n >= maxSummaries {
			l.skip(lib.ChipSummaries, "", "too_many", fmt.Sprintf("Only the first %d summaries go in one request.", maxSummaries))
			break
		}
		n++
		fmt.Fprintf(&b, "[doc:%s] %s: %s\n", id, clipText(nd.Title, 60), strings.Join(strings.Fields(s), " "))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// maxTimelineEvents bounds the events one request carries.
const maxTimelineEvents = 120

// timelineText formats the timeline's events and the findings of its
// deterministic check, as far as the caller may read them, with ids a finding
// may cite ("[event:ID]", "[finding:ID]").
func (l *loader) timelineText() (string, error) {
	f, err := timeline.AIFacts(l.ctx, l.d, l.p, l.projectID, maxTimelineEvents)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if n := f.ExcludedEvents + f.ExcludedFindings; n > 0 {
		l.skip(lib.ChipTimeline, "", "never_send", fmt.Sprintf("%d timeline event(s) and finding(s) that name something marked never-send were left out.", n))
	}
	if len(f.Events) > 0 {
		b.WriteString("Events, in story order:\n")
		for _, e := range f.Events {
			when := e.Date
			if e.End != "" {
				when += " to " + e.End
			}
			if when == "" {
				when = "undated"
			}
			fmt.Fprintf(&b, "[event:%s] %s | %s", e.ID, when, clipText(e.Title, 100))
			if e.Location != "" {
				b.WriteString(" | place: " + clipText(e.Location, 60))
			}
			if len(e.People) > 0 {
				b.WriteString(" | people: " + clipText(strings.Join(e.People, ", "), 200))
			}
			b.WriteString("\n")
		}
		if f.Truncated {
			l.skip(lib.ChipTimeline, "", "too_many", fmt.Sprintf("Only the first %d events go in one request.", maxTimelineEvents))
		}
	}
	if len(f.Findings) > 0 {
		b.WriteString("\nFindings of the timeline's own check:\n")
		for i, fd := range f.Findings {
			if i >= 60 {
				break
			}
			fmt.Fprintf(&b, "[finding:%s] (%s) %s\n", fd.ID, fd.Severity, clipText(fd.Message, 300))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
