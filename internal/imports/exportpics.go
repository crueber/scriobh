package imports

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

// The pictures of a Ghost or WordPress export. The export file names its
// pictures by address (https://site/content/images/2024/03/lamp.png, or
// __GHOST_URL__/content/images/..., or .../wp-content/uploads/2024/01/boat.jpg);
// they are never fetched. When the writer sends a zip that also holds the
// site's images folder (Ghost's content/images, WordPress's wp-content/uploads),
// a picture whose path after that folder is in the zip is imported from the
// zip. Only the part after the folder is compared, so the host in the address
// does not matter and nothing is resolved against it.

var (
	ghostSize = regexp.MustCompile(`^size/[a-z0-9]+/`)                     // Ghost's resized copies: content/images/size/w600/...
	wpSize    = regexp.MustCompile(`-[0-9]{1,5}x[0-9]{1,5}(\.[a-z0-9]+)$`) // WordPress's: boat-300x200.jpg
)

type exportPics struct {
	box   *picBox
	files map[string]File // "g/<path after content/images/>" or "w/<path after uploads/>"
}

// newExportPics indexes the pictures sent beside an export, or returns nil when
// there are none (then nothing changes: every image is a note, as before).
func newExportPics(files []File, box *picBox) *exportPics {
	e := &exportPics{box: box, files: map[string]File{}}
	for _, f := range files {
		p, why := cleanPath(f.Path)
		if why != "" || hidden(p) || f.Size < 0 {
			continue
		}
		l := strings.ToLower(p)
		if !isPictureName(l) {
			continue
		}
		switch {
		case strings.Contains(l, "content/images/"):
			e.files["g/"+l[strings.Index(l, "content/images/")+len("content/images/"):]] = f
		case strings.HasPrefix(l, "images/"):
			e.files["g/"+strings.TrimPrefix(l, "images/")] = f
		case strings.Contains(l, "uploads/"):
			e.files["w/"+l[strings.Index(l, "uploads/")+len("uploads/"):]] = f
		}
	}
	if len(e.files) == 0 {
		return nil
	}
	return e
}

// resolve makes the picture an address names, when the zip holds it.
func (e *exportPics) resolve(src, alt string) (seg, bool) {
	if e == nil {
		return seg{}, false
	}
	s := strings.TrimSpace(src)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	s = strings.ToLower(s)
	var keys []string
	switch {
	case strings.Contains(s, "/content/images/"):
		rest := ghostSize.ReplaceAllString(s[strings.Index(s, "/content/images/")+len("/content/images/"):], "")
		keys = append(keys, "g/"+rest)
	case strings.Contains(s, "/wp-content/uploads/"):
		rest := s[strings.Index(s, "/wp-content/uploads/")+len("/wp-content/uploads/"):]
		keys = append(keys, "w/"+rest, "w/"+wpSize.ReplaceAllString(rest, "$1"))
	default:
		return seg{}, false
	}
	for _, k := range keys {
		if strings.Contains(k, "..") {
			return seg{}, false
		}
		f, ok := e.files[k]
		if !ok {
			continue
		}
		p, _ := e.box.add(f.Path, f.bytesOf(), path.Base(f.Path))
		if p == nil {
			return seg{}, false
		}
		if alt == "" {
			alt = strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))
		}
		return p.seg(alt), true
	}
	e.box.missing++
	return seg{}, false
}
