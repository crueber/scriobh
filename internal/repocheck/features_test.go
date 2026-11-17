package repocheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	// A catalogue row: | F-123 | what it is | P0 | 1 |, with a priority in the third column.
	catalogueRow = regexp.MustCompile(`^\|\s*(F-\d+)\s*\|.*\|\s*(?:P[0-3]|X)\s*\|[^|]*\|\s*$`)
	// Any row that begins with exactly one id (a combined row such as "F-470 / F-471" is a reference, not a definition).
	idRow = regexp.MustCompile(`^\|\s*(F-\d+)\s*\|`)
)

// featureIDs reads docs/FEATURES.md and returns, for each F-id, the lines that define it.
//
// An id names one feature and is defined once: by its row in the catalogue (sections A to V, with a priority and a
// phase), or, for a feature that was only ever written down as it was built, by its single row in an implementation
// status section. A status row for an id that the catalogue defines is that feature's later news (Built, Partial)
// and is not a second definition; a section may carry several of them over the phases.
func featureIDs(t *testing.T, text string) (catalogue, statusOnly map[string][]int) {
	t.Helper()
	cat := map[string][]int{}
	status := map[string][]int{}
	for i, l := range strings.Split(text, "\n") {
		if m := catalogueRow.FindStringSubmatch(l); m != nil {
			cat[m[1]] = append(cat[m[1]], i+1)
		} else if m := idRow.FindStringSubmatch(l); m != nil {
			status[m[1]] = append(status[m[1]], i+1)
		}
	}
	only := map[string][]int{}
	for id, lines := range status {
		if _, defined := cat[id]; !defined {
			only[id] = lines
		}
	}
	return cat, only
}

// Two features must never share an F-id: a reference to one ("F-760") would mean either. The Phase 6 status sections
// once did (the static site and the webhook and MCP rows both numbered F-760; the imports and the site both
// F-770; the site page and the import page both F-780), and a renumbering of the later rows fixed it.
func TestFeatureIDsAreDefinedOnce(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "FEATURES.md"))
	if err != nil {
		t.Fatal(err)
	}
	cat, only := featureIDs(t, string(b))
	if len(cat) < 300 {
		t.Fatalf("found %d catalogue rows; has the table layout (| F-nnn | feature | Pri | Phase |) changed?", len(cat))
	}
	if len(only) < 50 {
		t.Fatalf("found %d status-only ids; has the status layout changed?", len(only))
	}
	report := func(kind string, m map[string][]int) {
		var ids []string
		for id, lines := range m {
			if len(lines) > 1 {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return idNumber(ids[i]) < idNumber(ids[j]) })
		for _, id := range ids {
			var at []string
			for _, l := range m[id] {
				at = append(at, strconv.Itoa(l))
			}
			t.Errorf("%s is defined %d times (%s, lines %s): give the later one an unused id and update what refers to it", id, len(m[id]), kind, strings.Join(at, ", "))
		}
	}
	report("catalogue rows", cat)
	report("status rows with no catalogue row", only)
}

func idNumber(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "F-"))
	return n
}

// The check itself: it names a duplicate in either place and lets a catalogue id carry later status rows.
func TestFeatureIDCheckSeesDuplicates(t *testing.T) {
	text := strings.Join([]string{
		"| F-001 | One | P0 | 1 |",
		"| F-002 | Two | P1 | 2 |",
		"| F-002 | Two again | P1 | 2 |",
		"| F-001 | Built | The first news. |",
		"| F-001 | Built | The second news. |",
		"| F-900 | Built | Only ever a status row. |",
		"| F-900 | Built | A different feature with the same id. |",
		"| F-901 / F-902 | Built | A combined row defines nothing. |",
	}, "\n")
	cat, only := featureIDs(t, text)
	if len(cat["F-002"]) != 2 || len(cat["F-001"]) != 1 {
		t.Errorf("catalogue = %v", cat)
	}
	if len(only["F-900"]) != 2 || len(only) != 1 {
		t.Errorf("status-only = %v", only)
	}
}
