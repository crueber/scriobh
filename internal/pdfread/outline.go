package pdfread

// OutlineItem is one bookmark: its text, its depth (0 at the top), and where it
// points: a page index (-1 when it does not resolve) and the height of the
// target above the page's bottom edge in the displayed page (-1 when none).
type OutlineItem struct {
	Title string
	Level int
	Page  int
	Y     float64
}

// Outline reads the document's bookmarks. Only a GoTo destination is followed;
// a URI, a launch or a JavaScript action is never read.
func (d *Doc) Outline() []OutlineItem {
	f := d.f
	ol, _ := f.Resolve(d.root["Outlines"]).(Dict)
	if ol == nil {
		return nil
	}
	var out []OutlineItem
	seen := map[int]bool{}
	var walk func(first any, level int)
	walk = func(first any, level int) {
		cur := first
		for n := 0; n < 5000 && cur != nil; n++ {
			ref, isRef := cur.(Ref)
			if isRef {
				if seen[ref.Num] {
					return
				}
				seen[ref.Num] = true
			}
			item, _ := f.Resolve(cur).(Dict)
			if !f.spendNode() {
				return
			}
			if item == nil || len(out) >= f.lim.OutlineItems || level > 12 {
				return
			}
			it := OutlineItem{Title: pdfText(f.Resolve(item["Title"])), Level: level, Page: -1, Y: -1}
			dest := f.Resolve(item["Dest"])
			if dest == nil {
				if a, ok := f.Resolve(item["A"]).(Dict); ok {
					if s, _ := f.Resolve(a["S"]).(Name); s == "GoTo" {
						dest = f.Resolve(a["D"])
					}
				}
			}
			it.Page, it.Y = d.destination(dest, 0)
			if it.Title != "" {
				out = append(out, it)
			}
			if kids := item["First"]; kids != nil {
				walk(kids, level+1)
			}
			cur = item["Next"]
		}
	}
	walk(ol["First"], 0)
	return out
}

// destination resolves a destination (an array, a name, or a dictionary with D).
func (d *Doc) destination(dest any, depth int) (int, float64) {
	f := d.f
	if depth > 4 {
		return -1, -1
	}
	switch v := dest.(type) {
	case Array:
		if len(v) == 0 {
			return -1, -1
		}
		page := -1
		switch p := v[0].(type) {
		case Ref:
			if i, ok := d.byObj[p.Num]; ok {
				page = i
			}
		case float64: // a remote-style index
			if i := fnum(p); i >= 0 && i < len(d.pages) {
				page = i
			}
		}
		y := -1.0
		if page >= 0 && len(v) >= 4 {
			if t, _ := f.Resolve(v[1]).(Name); t == "XYZ" {
				if top, ok := f.Resolve(v[3]).(float64); ok {
					y = d.displayY(page, 0, top)
				}
			} else if t == "FitH" || t == "FitBH" {
				if top, ok := f.Resolve(v[2]).(float64); ok {
					y = d.displayY(page, 0, top)
				}
			}
		}
		return page, y
	case Name:
		return d.destination(d.named(string(v)), depth+1)
	case String:
		return d.destination(d.named(string(v)), depth+1)
	case Dict:
		return d.destination(f.Resolve(v["D"]), depth+1)
	}
	return -1, -1
}

// displayY turns a y in user space into one in the displayed page.
func (d *Doc) displayY(page int, x, y float64) float64 {
	pr := d.pages[page]
	switch pr.rot {
	case 90:
		return pr.box[2] - pr.box[0] - (x - pr.box[0])
	case 180:
		return pr.box[3] - pr.box[1] - (y - pr.box[1])
	case 270:
		return x - pr.box[0]
	}
	return y - pr.box[1]
}

// named looks up a named destination in the catalog's Dests dictionary or in
// the Names tree. The tree is read once, into a table, however many bookmarks
// ask: a node is visited once and the file shares a budget of nodes.
func (d *Doc) named(name string) any {
	f := d.f
	if d.dests == nil {
		d.dests = map[string]any{}
		if dd, ok := f.Resolve(d.root["Dests"]).(Dict); ok {
			n := 0
			for k, v := range dd {
				if n++; n > 100_000 || !f.spendNode() || !f.spendWork(1) {
					break
				}
				d.dests[k] = f.Resolve(v)
			}
		}
		if names, ok := f.Resolve(d.root["Names"]).(Dict); ok {
			if dt, ok := f.Resolve(names["Dests"]).(Dict); ok {
				d.flattenDests(dt, map[int]bool{}, 0)
			}
		}
	}
	return d.dests[name]
}

func (d *Doc) flattenDests(n Dict, seen map[int]bool, depth int) {
	f := d.f
	if depth > 8 || !f.spendNode() {
		return
	}
	if arr, ok := f.Resolve(n["Names"]).(Array); ok {
		if !f.spendWork(len(arr) / 2) {
			return
		}
		for i := 0; i+1 < len(arr) && len(d.dests) < 100_000; i += 2 {
			if k, ok := f.Resolve(arr[i]).(String); ok {
				if _, have := d.dests[string(k)]; !have {
					d.dests[string(k)] = f.Resolve(arr[i+1])
				}
			}
		}
	}
	if kids, ok := f.Resolve(n["Kids"]).(Array); ok {
		if !f.spendWork(min(len(kids), 2001)) {
			return
		}
		for i, k := range kids {
			if i > 2000 {
				break
			}
			if ref, isRef := k.(Ref); isRef {
				if seen[ref.Num] {
					continue
				}
				seen[ref.Num] = true
			}
			if kd, ok := f.Resolve(k).(Dict); ok {
				d.flattenDests(kd, seen, depth+1)
			}
		}
	}
}
