// Package check validates the files internal/publish writes, with no tool
// outside the Go standard library: a DOCX against the subset of OOXML it emits
// (well-formed parts, content types, relationships that resolve and never
// leave the package, styles and numbering that exist, schema order of
// properties), and an EPUB against the structure EPUB 3 requires (mimetype
// first and stored, container, package document, manifest and spine, a
// navigation document, well-formed XHTML, links that resolve).
//
// This is a structural check. It is not Word, and it is not epubcheck: it
// proves the files are what this package means them to be, not that every
// reader accepts them.
package check

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// node is a parsed XML element.
type node struct {
	space, name string
	attrs       []xml.Attr
	kids        []*node
	text        string
}

func (n *node) attr(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func (n *node) attrNS(space, local string) (string, bool) {
	for _, a := range n.attrs {
		if a.Name.Local == local && a.Name.Space == space {
			return a.Value, true
		}
	}
	return "", false
}

func (n *node) child(local string) *node {
	for _, k := range n.kids {
		if k.name == local {
			return k
		}
	}
	return nil
}

func (n *node) walk(fn func(*node)) {
	fn(n)
	for _, k := range n.kids {
		k.walk(fn)
	}
}

// parseXML parses a part strictly: well-formed, one root, no DOCTYPE or
// processing instruction other than the XML declaration, no entities beyond the
// five predefined ones.
func parseXML(data []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil
	var root *node
	var stack []*node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{space: t.Name.Space, name: t.Name.Local, attrs: t.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("more than one root element")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		case xml.Directive:
			return nil, fmt.Errorf("a DOCTYPE or other directive is not allowed")
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, fmt.Errorf("processing instruction %q is not allowed", t.Target)
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("no root element")
	}
	return root, nil
}

// archive is an opened zip.
type archive struct {
	order []string
	files map[string][]byte
	zr    *zip.Reader
	probs []string
}

func (a *archive) problem(format string, args ...any) {
	a.probs = append(a.probs, fmt.Sprintf(format, args...))
}

// maxPart bounds what the checker will inflate from one entry.
const maxPart = 256 << 20

func openZip(data []byte) (*archive, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a readable zip: %w", err)
	}
	a := &archive{files: map[string][]byte{}, zr: zr}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if _, dup := a.files[f.Name]; dup {
			a.problem("duplicate entry %s", f.Name)
			continue
		}
		if f.Name == "" || strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "..") || strings.Contains(f.Name, "\\") {
			a.problem("unsafe entry name %q", f.Name)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			a.problem("cannot open %s: %v", f.Name, err)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxPart))
		rc.Close()
		if err != nil {
			a.problem("cannot read %s: %v", f.Name, err)
			continue
		}
		a.order = append(a.order, f.Name)
		a.files[f.Name] = b
	}
	return a, nil
}

// resolve joins a relationship or href target to the directory of the part
// that holds it.
func resolve(from, target string) string {
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i]
	}
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return path.Clean(path.Join(path.Dir(from), target))
}
