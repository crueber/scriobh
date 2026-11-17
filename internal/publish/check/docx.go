package check

import (
	"path"
	"sort"
	"strings"
)

const (
	nsW   = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsR   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsRel = "http://schemas.openxmlformats.org/package/2006/relationships"
)

// The schema order of the children of the property elements this package
// writes. Word rejects a file whose properties come out of order.
var order = map[string][]string{
	"pPr": {"pStyle", "keepNext", "keepLines", "pageBreakBefore", "framePr", "widowControl", "numPr", "suppressLineNumbers", "pBdr", "shd", "tabs",
		"suppressAutoHyphens", "kinsoku", "wordWrap", "overflowPunct", "topLinePunct", "autoSpaceDE", "autoSpaceDN", "bidi", "adjustRightInd",
		"snapToGrid", "spacing", "ind", "contextualSpacing", "mirrorIndents", "suppressOverlap", "jc", "textDirection", "textAlignment",
		"textboxTightWrap", "outlineLvl", "divId", "cnfStyle", "rPr", "sectPr", "pPrChange"},
	"rPr": {"rStyle", "rFonts", "b", "bCs", "i", "iCs", "caps", "smallCaps", "strike", "dstrike", "outline", "shadow", "emboss", "imprint", "noProof",
		"snapToGrid", "vanish", "webHidden", "color", "spacing", "w", "kern", "position", "sz", "szCs", "highlight", "u", "effect", "bdr", "shd",
		"fitText", "vertAlign", "rtl", "cs", "em", "lang", "eastAsianLayout", "specVanish", "oMath"},
	"sectPr": {"headerReference", "footerReference", "footnotePr", "endnotePr", "type", "pgSz", "pgMar", "paperSrc", "pgBorders", "lnNumType",
		"pgNumType", "cols", "formProt", "vAlign", "noEndnote", "titlePg", "textDirection", "bidi", "rtlGutter", "docGrid", "printerSettings"},
	"tblPr": {"tblStyle", "tblpPr", "tblOverlap", "bidiVisual", "tblStyleRowBandSize", "tblStyleColBandSize", "tblW", "jc", "tblCellSpacing", "tblInd",
		"tblBorders", "shd", "tblLayout", "tblCellMar", "tblLook", "tblCaption", "tblDescription"},
	"tcPr":  {"cnfStyle", "tcW", "gridSpan", "hMerge", "vMerge", "tcBorders", "shd", "noWrap", "tcMar", "textDirection", "tcFitText", "vAlign", "hideMark"},
	"style": {"name", "aliases", "basedOn", "next", "link", "autoRedefine", "hidden", "uiPriority", "semiHidden", "unhideWhenUsed", "qFormat", "locked", "personal", "personalCompose", "personalReply", "rsid", "pPr", "rPr", "tblPr", "trPr", "tcPr", "tblStylePr"},
	"settings": {"writeProtection", "view", "zoom", "removePersonalInformation", "removeDateAndTime", "doNotDisplayPageBoundaries", "displayBackgroundShape",
		"printPostScriptOverText", "printFractionalCharacterWidth", "printFormsData", "embedTrueTypeFonts", "embedSystemFonts", "saveSubsetFonts",
		"saveFormsData", "mirrorMargins", "alignBordersAndEdges", "bordersDoNotSurroundHeader", "bordersDoNotSurroundFooter", "gutterAtTop",
		"hideSpellingErrors", "hideGrammaticalErrors", "activeWritingStyle", "proofState", "formsDesign", "attachedTemplate", "linkStyles",
		"stylePaneFormatFilter", "stylePaneSortMethod", "documentType", "mailMerge", "revisionView", "trackRevisions", "doNotTrackMoves",
		"doNotTrackFormatting", "documentProtection", "autoFormatOverride", "styleLockTheme", "styleLockQFSet", "defaultTabStop", "autoHyphenation",
		"consecutiveHyphenLimit", "hyphenationZone", "doNotHyphenateCaps", "showEnvelope", "summaryLength", "clickAndTypeStyle", "defaultTableStyle",
		"evenAndOddHeaders", "bookFoldRevPrinting", "bookFoldPrinting", "bookFoldPrintingSheets", "drawingGridHorizontalSpacing",
		"drawingGridVerticalSpacing", "displayHorizontalDrawingGridEvery", "displayVerticalDrawingGridEvery", "doNotUseMarginsForDrawingGridOrigin",
		"drawingGridHorizontalOrigin", "drawingGridVerticalOrigin", "doNotShadeFormData", "noPunctuationKerning", "characterSpacingControl",
		"printTwoOnOne", "strictFirstAndLastChars", "noLineBreaksAfter", "noLineBreaksBefore", "savePreviewPicture", "doNotValidateAgainstSchema",
		"saveInvalidXml", "ignoreMixedContent", "alwaysShowPlaceholderText", "doNotDemarcateInvalidXml", "saveXmlDataOnly", "useXSLTWhenSaving",
		"saveThroughXslt", "showXMLTags", "alwaysMergeEmptyNamespace", "updateFields", "hdrShapeDefaults", "footnotePr", "endnotePr", "compat",
		"docVars", "rsids", "mathPr", "attachedSchema", "themeFontLang", "clrSchemeMapping", "doNotIncludeSubdocsInStats",
		"doNotAutoCompressPictures", "forceUpgrade", "captions", "readModeInkLockDown", "smartTagType", "schemaLibrary", "shapeDefaults",
		"doNotEmbedSmartTags", "decimalSymbol", "listSeparator"},
}

// forbiddenParts are never in a package this program writes.
func forbiddenPart(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "vbaproject") || strings.HasSuffix(l, ".bin") || strings.Contains(l, "/embeddings/") || strings.Contains(l, "activex") ||
		strings.HasSuffix(l, ".exe") || strings.HasSuffix(l, ".dll") || strings.HasSuffix(l, ".js") || strings.HasSuffix(l, ".vbs") ||
		strings.Contains(l, "customui") || strings.HasSuffix(l, ".docm") || strings.Contains(l, "/macros/")
}

// forbiddenRelType are relationship types that point out of the document or at
// code. A hyperlink relationship is one: this package writes links as text.
func forbiddenRelType(t string) bool {
	l := strings.ToLower(t)
	for _, bad := range []string{"oleobject", "vbaproject", "macro", "externallink", "attachedtemplate", "subdocument", "activex"} {
		if strings.Contains(l, bad) {
			return true
		}
	}
	for _, bad := range []string{"/hyperlink", "/package", "/frame", "/control"} {
		if strings.HasSuffix(l, bad) {
			return true
		}
	}
	return false
}

// DOCX returns what is wrong with a DOCX written by internal/publish, or nil.
func DOCX(data []byte) []string {
	a, err := openZip(data)
	if err != nil {
		return []string{err.Error()}
	}
	if len(a.order) == 0 || a.order[0] != "[Content_Types].xml" {
		a.problem("[Content_Types].xml must be the first entry")
	}
	trees := map[string]*node{}
	for _, name := range a.order {
		if forbiddenPart(name) {
			a.problem("forbidden part %s", name)
		}
		if strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels") {
			t, err := parseXML(a.files[name])
			if err != nil {
				a.problem("%s is not well-formed XML: %v", name, err)
				continue
			}
			trees[name] = t
		}
	}
	a.contentTypes(trees)
	rels := a.relationships(trees)
	a.document(trees, rels)
	for name, t := range trees {
		if strings.HasPrefix(name, "word/") && !strings.HasSuffix(name, ".rels") {
			a.schemaOrder(name, t)
		}
	}
	sort.Strings(a.probs)
	return a.probs
}

// contentTypes checks every part has a declared type.
func (a *archive) contentTypes(trees map[string]*node) {
	ct := trees["[Content_Types].xml"]
	if ct == nil {
		a.problem("no [Content_Types].xml")
		return
	}
	defaults := map[string]bool{}
	overrides := map[string]bool{}
	for _, k := range ct.kids {
		switch k.name {
		case "Default":
			defaults[strings.ToLower(k.attr("Extension"))] = true
		case "Override":
			overrides[k.attr("PartName")] = true
		}
	}
	for p := range overrides {
		if _, ok := a.files[strings.TrimPrefix(p, "/")]; !ok {
			a.problem("content type override for a part that is not there: %s", p)
		}
	}
	for _, name := range a.order {
		if name == "[Content_Types].xml" {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		if !overrides["/"+name] && !defaults[ext] {
			a.problem("part %s has no content type", name)
		}
	}
	for _, need := range []string{"/word/document.xml", "/word/styles.xml"} {
		if !overrides[need] {
			a.problem("no content type for %s", need)
		}
	}
}

// relationships checks every .rels file and returns, for each part, its
// relationships by id.
func (a *archive) relationships(trees map[string]*node) map[string]map[string]string {
	out := map[string]map[string]string{}
	for name, t := range trees {
		if !strings.HasSuffix(name, ".rels") {
			continue
		}
		if t.name != "Relationships" || t.space != nsRel {
			a.problem("%s is not a relationships part", name)
			continue
		}
		owner := path.Join(path.Dir(path.Dir(name)), strings.TrimSuffix(path.Base(name), ".rels"))
		if name == "_rels/.rels" {
			owner = ""
		}
		ids := map[string]string{}
		for _, r := range t.kids {
			id, target, typ, mode := r.attr("Id"), r.attr("Target"), r.attr("Type"), r.attr("TargetMode")
			if id == "" || target == "" || typ == "" {
				a.problem("%s: a relationship lacks Id, Type or Target", name)
				continue
			}
			if _, dup := ids[id]; dup {
				a.problem("%s: duplicate relationship id %s", name, id)
			}
			if strings.EqualFold(mode, "External") {
				a.problem("%s: external relationship %s -> %s", name, id, target)
			}
			if forbiddenRelType(typ) {
				a.problem("%s: forbidden relationship type %s", name, typ)
			}
			if strings.Contains(target, "://") {
				a.problem("%s: relationship %s targets a URL: %s", name, id, target)
			}
			var tp string
			if owner == "" {
				tp = resolve("x", target)
			} else {
				tp = resolve(owner, target)
			}
			if _, ok := a.files[tp]; !ok {
				a.problem("%s: relationship %s targets %s, which is not in the package", name, id, tp)
			}
			ids[id] = tp
		}
		out[owner] = ids
	}
	if out[""] == nil {
		a.problem("no package relationships")
	}
	return out
}

// document checks that everything document.xml uses is defined.
func (a *archive) document(trees map[string]*node, rels map[string]map[string]string) {
	doc := trees["word/document.xml"]
	styles := trees["word/styles.xml"]
	if doc == nil || styles == nil {
		a.problem("word/document.xml or word/styles.xml is missing")
		return
	}
	if doc.name != "document" || doc.space != nsW {
		a.problem("word/document.xml is not a w:document")
	}
	styleIDs := map[string]string{}
	styles.walk(func(n *node) {
		if n.name == "style" && n.space == nsW {
			styleIDs[n.attr("styleId")] = n.attr("type")
		}
	})
	styles.walk(func(n *node) {
		if n.space != nsW {
			return
		}
		if n.name == "basedOn" || n.name == "next" || n.name == "link" {
			if _, ok := styleIDs[n.attr("val")]; !ok {
				a.problem("styles.xml: %s refers to the missing style %s", n.name, n.attr("val"))
			}
		}
	})
	for _, need := range []string{"Normal", "Heading1", "Title", "BodyText", "FirstParagraph", "SceneBreak"} {
		if _, ok := styleIDs[need]; !ok {
			a.problem("styles.xml has no %s style", need)
		}
	}
	numIDs := map[string]bool{}
	if num := trees["word/numbering.xml"]; num != nil {
		for _, k := range num.kids {
			if k.name == "num" {
				numIDs[k.attr("numId")] = true
			}
		}
	}
	noteIDs := map[string]bool{}
	if fn := trees["word/footnotes.xml"]; fn != nil {
		for _, k := range fn.kids {
			if k.name == "footnote" {
				noteIDs[k.attr("id")] = true
			}
		}
		if !noteIDs["-1"] || !noteIDs["0"] {
			a.problem("footnotes.xml lacks its separator footnotes")
		}
	}
	docRels := rels["word/document.xml"]
	docPr := map[string]bool{}
	used := map[string]bool{}
	for _, part := range []string{"word/document.xml", "word/footnotes.xml", "word/header1.xml", "word/header2.xml", "word/footer1.xml", "word/footer2.xml"} {
		t := trees[part]
		if t == nil {
			continue
		}
		own := rels[part]
		t.walk(func(n *node) {
			for _, at := range n.attrs {
				if at.Name.Space == nsR {
					if _, ok := own[at.Value]; !ok {
						a.problem("%s: r:%s=%s has no relationship", part, at.Name.Local, at.Value)
					}
					used[at.Value] = true
				}
			}
			if n.space == nsW {
				switch n.name {
				case "pStyle", "rStyle", "tblStyle":
					if _, ok := styleIDs[n.attr("val")]; !ok {
						a.problem("%s: %s uses the undefined style %s", part, n.name, n.attr("val"))
					}
				case "numId":
					if n.attr("val") != "0" && !numIDs[n.attr("val")] {
						a.problem("%s: numId %s is not defined", part, n.attr("val"))
					}
				case "footnoteReference":
					if !noteIDs[n.attr("id")] {
						a.problem("%s: footnote %s is not defined", part, n.attr("id"))
					}
				}
			}
			if n.name == "docPr" {
				id := n.attr("id")
				if docPr[id] {
					a.problem("%s: duplicate drawing id %s", part, id)
				}
				docPr[id] = true
			}
		})
	}
	// every image relationship is used
	for id, target := range docRels {
		if strings.HasPrefix(target, "word/media/") && !used[id] {
			a.problem("word/document.xml.rels: image %s is not used", id)
		}
	}
}

// schemaOrder checks the order of the children of the property elements.
func (a *archive) schemaOrder(name string, t *node) {
	t.walk(func(n *node) {
		if n.space != nsW {
			return
		}
		want, ok := order[n.name]
		if !ok {
			return
		}
		last := -1
		for _, k := range n.kids {
			idx := -1
			for i, w := range want {
				if w == k.name {
					idx = i
					break
				}
			}
			if idx < 0 {
				a.problem("%s: %s has the unexpected child %s", name, n.name, k.name)
				continue
			}
			if idx < last {
				a.problem("%s: in %s, %s is out of schema order", name, n.name, k.name)
			}
			last = idx
		}
	})
}
