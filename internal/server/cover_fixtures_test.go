package server

import (
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
)

// Fixtures for the cover page: what the template lint, the structure checks and the stylesheet budget render.

func coverFixtureSide(part, mode string) coverSide {
	sd := coverSide{Part: part, Mode: mode, Fit: "contain", Scale: "100", Pos: "center", Bg: "#2F5D50", Margin: "0"}
	if part == "back" && mode == "none" {
		return sd
	}
	if mode == "image" || mode == "page" {
		sd.Att = "att_" + part
		sd.Pic = &coverPic{ID: sd.Att, URL: "/files/" + sd.Att, W: 1200, H: 1800, DPI: 200}
	}
	return sd
}

func coverFixture(kind string) coverPageData {
	proj := fixtureProjects()[0]
	proj.Role = "owner"
	d := draftOf(nil, proj)
	d.CSRF, d.CanEdit, d.CoverClass, d.Kind, d.KindLabel, d.MaxPicture, d.Paper = "tok_writer", true, "cover-0", proj.Kind, "Novel", "25", "0.002252"
	d.Messages = map[string]string{"sending": "Sending {name}…"}
	for _, n := range coverlayout.Trims {
		t, _ := coverlayout.LookupTrim(n)
		d.Trims = append(d.Trims, coverTrim{Value: n, Label: n, W: fnum(t.W), H: fnum(t.H), Selected: n == d.Trim})
	}
	d.TrimW, d.TrimH = "6", "9"
	for i, sw := range projects.Swatches() {
		d.Palette = append(d.Palette, coverSwatch{Hex: sw.Hex, Class: sw.Class, Name: coverColours[i], Checked: i == 0})
	}
	for _, p := range coverlayout.Positions {
		d.Positions = append(d.Positions, coverPos{Value: p, Label: p, Checked: p == "center"})
	}
	switch kind {
	case "reader":
		d.CanEdit, d.Has, d.Shape, d.SavedAlt = false, true, "sides", "A red door in the snow"
	case "image":
		d.Has, d.Revision, d.Shape, d.Alt, d.SavedAlt = true, 1, "sides", "A red door in the snow", "A red door in the snow"
		d.Front, d.Shown = coverFixtureSide("front", "image"), true
		d.FrontURL, d.ThumbURL, d.Flash = "/api/v1/projects/prj_x/cover?v=a", "/api/v1/projects/prj_x/cover?size=thumb&v=a", "The cover is saved."
		d.Warnings = []coverNote{{Part: "front", Text: "The front picture is 200 dpi."}}
	case "page":
		d.Has, d.Revision, d.Shape, d.Alt, d.Dirty = true, 2, "sides", "A red door in the snow", true
		d.Front, d.Back = coverFixtureSide("front", "page"), coverFixtureSide("back", "image")
		d.Front.Fit, d.Front.Scale, d.Front.Pos, d.Front.Margin = "cover", "60", "top-left", "0.5"
		d.FrontSvg = &coverSvg{CW: 1707, CH: 2560, X: 100, Y: 100, W: 900, H: 1350, BX: 100, BY: 100, BW: 1507, BH: 2360, Page: true, Href: "/files/att_front", Class: "cover-0"}
		d.Notes = []coverNote{{Part: "front", Text: "Added front.jpg."}}
	case "wrap":
		d.Has, d.Revision, d.Shape, d.Alt, d.Shown = true, 3, "wrap", "A harbour at dusk", true
		d.Wrap.Att, d.Bleed = "att_wrap", "0.125"
		d.FrontURL, d.ThumbURL, d.BackURL = "/api/v1/projects/prj_x/cover?v=b", "/api/v1/projects/prj_x/cover?size=thumb&v=b", "/api/v1/projects/prj_x/cover?part=back&v=b"
		d.Wrap = coverWrapView{Att: "att_wrap", URL: "/files/att_wrap", PW: 1913, PH: 1388, DPI: 150, WidthIn: "12.75", SpineIn: "0.5", SpinePages: 222, BleedIn: "0.125", FrontOn: "right", Measured: true,
			Cut: coverBox{19, 19, 1875, 1350}, MarginBack: coverBox{56, 56, 825, 1275}, MarginFront: coverBox{1050, 56, 825, 1275}, Fold0: 937, Fold1: 1012, FS: 49, LabelY: 117,
			Labels: [3]coverLabel{{"back", 478, true}, {"spine", 975, true}, {"front", 1462, true}}, FrontPreview: "1012 19 862 1331"}
	case "error":
		d.Dirty, d.Shape, d.Alt = true, "sides", ""
		d.Front = coverFixtureSide("front", "image")
		d.FrontSvg = &coverSvg{CW: 1707, CH: 2560, Href: "/files/att_front", Class: "cover-0"}
		d.Error, d.ErrorFor = "Describe the cover in a sentence. A screen reader says it where the picture is.", "cv-alt"
	case "conflict":
		d.Has, d.Revision, d.Conflict, d.Error = true, 4, true, "Someone else changed the cover while you had this page open."
		d.Yours = &coverVersion{Shape: "Front and back", Trim: "6 × 9 in", Alt: "A red door"}
		d.Saved = &coverVersion{Shape: "Front and back", Trim: "6 × 9 in", Alt: "A blue door", Shown: true, Thumb: "/api/v1/projects/prj_x/cover?size=thumb&v=a"}
	}
	return d
}

func coverPageFixtures() []fixture {
	var out []fixture
	for _, k := range []string{"generated", "reader", "image", "page", "wrap", "error", "conflict"} {
		out = append(out, fixture{"cover", p5With("Cover · The Salt Road", "/p/the-salt-road/cover", coverFixture(k))})
	}
	return out
}
