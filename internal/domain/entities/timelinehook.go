package entities

import (
	"context"
	"sync"
)

// The timeline lives in a Compendium, so its file is written by the
// Compendium's mirror and export, but it is built by a package that imports
// this one (it reads entities). The timeline package therefore registers
// itself here once, at start, and the renderers and fingerprints below call it.
// Without one registered (a schema-only build) the files hold the placeholder
// and the fingerprints are what they were.

// TimelineHook is what the timeline package supplies.
type TimelineHook struct {
	// Compendium renders timeline.md for one Compendium: all its events in
	// story order. It returns nil when the Compendium has no events.
	Compendium func(ctx context.Context, q Querier, compendiumID string) ([]byte, error)
	// Project renders compendium/timeline.md of a project: the events of the
	// Compendiums it draws on in story order, with the scenes that tell them
	// and the book's own order. nil when there is nothing to say.
	Project func(ctx context.Context, q Querier, projectID string) ([]byte, error)
	// CompendiumStamps hashes what each Compendium's timeline file depends on,
	// by Compendium id; a Compendium with no events is absent. only limits it
	// to those ids (nil means all).
	CompendiumStamps func(ctx context.Context, q Querier, only []string) (map[string]string, error)
	// ProjectStamps hashes what a project's timeline file depends on beyond its
	// Compendiums' events (the titles of the scenes that tell them, the book's
	// order), by project id; a project with nothing is absent. only limits it
	// to one project ("" means all).
	ProjectStamps func(ctx context.Context, q Querier, only string) (map[string]string, error)
}

var (
	timelineMu   sync.RWMutex
	timelineHook *TimelineHook
)

// RegisterTimeline installs the timeline's renderers and fingerprints.
func RegisterTimeline(h TimelineHook) {
	timelineMu.Lock()
	defer timelineMu.Unlock()
	timelineHook = &h
}

func timeline() *TimelineHook {
	timelineMu.RLock()
	defer timelineMu.RUnlock()
	return timelineHook
}

// timelineFile is the content of a Compendium's timeline.md.
func timelineFile(ctx context.Context, q Querier, compendiumID string) ([]byte, error) {
	if h := timeline(); h != nil && h.Compendium != nil {
		b, err := h.Compendium(ctx, q, compendiumID)
		if err != nil {
			return nil, err
		}
		if len(b) > 0 {
			return b, nil
		}
	}
	return []byte(timelinePlaceholder), nil
}

// projectTimelineFile is the content of a project's compendium/timeline.md.
func projectTimelineFile(ctx context.Context, q Querier, projectID string) ([]byte, error) {
	if h := timeline(); h != nil && h.Project != nil {
		b, err := h.Project(ctx, q, projectID)
		if err != nil {
			return nil, err
		}
		if len(b) > 0 {
			return b, nil
		}
	}
	return []byte(timelinePlaceholder), nil
}
