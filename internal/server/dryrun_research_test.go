package server

import (
	"encoding/json"
	"strconv"
	"testing"
)

// researchFixtures drives the research, tag, scratchpad and attachment ops
// through the dry-run sweep, for a project and for a Compendium. Every one is a
// preview that changes no row and no file, except the uploads, which write a
// file and so must refuse a preview (noPreview lists them).
func (f *dryFixture) researchFixtures(t *testing.T) map[string]drive {
	t.Helper()
	post := func(path, body string) []byte {
		t.Helper()
		rec := f.srv.do(f.owner, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("fixture POST %s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.Bytes()
	}
	t.Cleanup(func() { sweepChildren = map[string]string{} })
	cmp := idOf(t, post("/api/v1/compendiums", `{"name":"Dry Research Bible"}`))
	out := map[string]drive{}
	for _, o := range []struct{ prefix, base string }{
		{"project", "/api/v1/projects/" + f.project},
		{"compendium", "/api/v1/compendiums/" + cmp},
	} {
		f.srv.sweepFixtures(t, f.owner, o.prefix, map[string]string{"project": f.project, "compendium": cmp}[o.prefix])
		ch := sweepChildren
		folder := idOf(t, post(o.base+"/research", `{"title":"A folder","kind":"folder"}`))
		spare := idOf(t, post(o.base+"/research", `{"title":"Spare note","body":"Words."}`))
		trashed := idOf(t, post(o.base+"/research", `{"title":"Old note"}`))
		post(o.base+"/research/"+trashed+"/trash", "")
		spareTag := idOf2(t, post(o.base+"/tags", `{"name":"spare"}`))
		var cur struct{ Item struct{ Revision int } }
		_ = json.Unmarshal([]byte(mustGet(t, f, o.base+"/research/"+spare)), &cur)
		rev := strconv.Itoa(cur.Item.Revision)
		var scratch struct{ Item struct{ Revision int } }
		_ = json.Unmarshal([]byte(mustGet(t, f, o.base+"/scratchpad")), &scratch)
		p := o.prefix + "."
		out[p+"research.create"] = drive{o.base + "/research", `{"title":"Previewed note","body":"Words","tags":["new"]}`, true}
		out[p+"research.update"] = drive{o.base + "/research/" + spare, `{"base_revision":` + rev + `,"body":"Changed words","tags":["spare"]}`, true}
		out[p+"research.move"] = drive{o.base + "/research/" + spare + "/move", `{"parent_id":"` + folder + `"}`, true}
		out[p+"research.trash"] = drive{o.base + "/research/" + spare + "/trash", "", true}
		out[p+"research.restore"] = drive{o.base + "/research/" + trashed + "/restore", "", true}
		out[p+"tag.create"] = drive{o.base + "/tags", `{"name":"previewed","color":"moss"}`, true}
		out[p+"tag.update"] = drive{o.base + "/tags/" + spareTag, `{"name":"renamed","color":"plum"}`, true}
		out[p+"tag.merge"] = drive{o.base + "/tags/" + spareTag + "/merge", `{"into_id":"` + ch["tag_id"] + `"}`, true}
		out[p+"tag.delete"] = drive{o.base + "/tags/" + spareTag, "", true}
		out[p+"scratch.set"] = drive{o.base + "/scratchpad", `{"content":"Previewed pad","base_revision":` + strconv.Itoa(scratch.Item.Revision) + `}`, true}
		out[p+"attachment.update"] = drive{o.base + "/attachments/" + ch["att_id"], `{"alt":"Previewed alt"}`, true}
		out[p+"attachment.delete"] = drive{o.base + "/attachments/" + ch["att_id"] + "?force=true", "", true}
		out[p+"attachment.upload"] = drive{o.base + "/attachments?filename=a.png", "", false}
	}
	return out
}

func idOf2(t *testing.T, body []byte) string { return idOf(t, body) }

func mustGet(t *testing.T, f *dryFixture, path string) string {
	t.Helper()
	rec := f.srv.do(f.owner, "GET", path, "")
	if rec.Code >= 300 {
		t.Fatalf("fixture GET %s: %d %s", path, rec.Code, rec.Body)
	}
	return rec.Body.String()
}
