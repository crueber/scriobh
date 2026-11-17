package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// A Compendium that no project uses as its primary has a mirror of its own, so
// "a Compendium can be created, edited and exported with no project open" has
// files to show for it:
//
//	compendium-<slug>--<id suffix>/
//	  compendium.json     manifest: settings, field definitions, references, every entity and its hash
//	  README.md           says the folder is generated
//	  entities/<kind>/    one Markdown file per entity, with front matter
//	  timeline.md         placeholder until the timeline is written
//
// A Compendium that is some project's primary has no folder of its own: its
// entities are in that project's compendium/ folder (with that book's
// overrides), and the standalone folder is removed when a project takes it
// over, and written again when the last such project lets it go.
//
// The machinery is the project's: the same state record under .state/, the same
// per-file diffing, debounce, retry and start-up check. Only what is rendered
// and what fingerprints it differ.

const (
	// CompendiumManifestName is a standalone Compendium folder's manifest.
	CompendiumManifestName = "compendium.json"
	projectionPrefix       = "compendium/"
	compendiumDirPrefix    = "compendium-"
)

// carried reports whether a recorded file is part of the compendium/ entity
// projection, which a flush carries forward while its fingerprint is unchanged.
// The research under compendium/research/ is not: it is rendered every time.
func carried(name string) bool {
	return strings.HasPrefix(name, projectionPrefix) && !strings.HasPrefix(name, projectResearch)
}

// isCompendiumID reports whether a mirror key names a Compendium. Project ids
// start prj_, Compendium ids cmp_.
func isCompendiumID(id string) bool { return strings.HasPrefix(id, "cmp_") }

// compendiumDirName is the folder of a standalone Compendium: slugs are unique
// per owner only, so the id's last six characters make the name unambiguous.
func compendiumDirName(id, slug string) string {
	s := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(slug), "-"), "-")
	if s == "" {
		s = "compendium"
	}
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return compendiumDirPrefix + s + "--" + idSuffix(id)
}

// CompendiumPurged is the purge hook for a Compendium: it cancels pending work
// and removes the folder. The Compendium is gone from the database, so the
// flush that follows finds nothing to write and removes what it recorded.
func (m *Mirror) CompendiumPurged(ctx context.Context, compendiumID string) {
	m.ProjectPurged(ctx, compendiumID)
}

// flushCompendium brings one standalone Compendium's folder in line with the
// database, or removes it when the Compendium is gone or a project now uses it.
func (m *Mirror) flushCompendium(ctx context.Context, p *project, id string, st *diskState, force bool) (changed, removed bool, err error) {
	fps, err := compendiumFingerprints(ctx, m.o.DB.Read(), id)
	if err != nil {
		return false, false, err
	}
	fp, live := fps[id]
	if !live {
		ch, err := m.removeProject(p, st)
		return ch, true, err
	}
	set, err := entities.LoadCompendium(ctx, m.o.DB, id, CompendiumManifestName, "scriobh-compendium", false)
	if err != nil {
		// Only a Compendium that is really gone loses its folder. A busy read or
		// a cancelled context is retried, as it is for a project.
		if ops.AsError(err).Code == ops.CodeNotFound {
			ch, rerr := m.removeProject(p, st)
			return ch, true, rerr
		}
		return false, false, err
	}
	if !set.Meta.Standalone {
		ch, err := m.removeProject(p, st)
		return ch, true, err
	}
	files := set.MirrorFiles()
	exFiles, ext, _, err := m.extras(ctx, id)
	if err != nil {
		return false, false, err
	}
	for n, b := range exFiles {
		files[n] = b
	}
	next, changedPaths, err := m.apply(id, st, compendiumDirName(id, set.DirSlug()), files, ext, false, force)
	next.Fingerprint = fp
	if err != nil {
		next.Fingerprint = ""
	}
	if len(changedPaths) > 0 {
		next.WrittenAt = m.stamp()
	}
	if serr := m.saveState(p, id, next); serr != nil && err == nil {
		err = fmt.Errorf("recording what was written: %s", cause(serr))
	}
	return len(changedPaths) > 0, false, err
}

// findCompendiumDir looks for a folder that is plainly this Compendium's when
// no state says where it is: the name ends with the id suffix and the manifest
// carries the id.
func (m *Mirror) findCompendiumDir(id string) string {
	entries, err := m.fs.ReadDir(m.root)
	if err != nil {
		return ""
	}
	suffix := "--" + idSuffix(id)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), suffix) || !strings.HasPrefix(e.Name(), compendiumDirPrefix) {
			continue
		}
		b, err := m.fs.ReadFile(filepath.Join(m.root, e.Name(), CompendiumManifestName))
		if err != nil {
			continue
		}
		var mf struct {
			Compendium struct {
				ID string `json:"id"`
			} `json:"compendium"`
		}
		if json.Unmarshal(b, &mf) == nil && mf.Compendium.ID == id {
			return e.Name()
		}
	}
	return ""
}
