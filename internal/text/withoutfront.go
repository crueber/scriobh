package text

// WithoutFrontMatter returns md with its front matter, a leading `---` ... `---` block, taken out, with the line
// endings made LF as the parser sees them. A caller that must remove other constructs first (private notes, which
// can be left open and so cut a closing `---` away) takes the front matter out before it does.
func WithoutFrontMatter(md string) string { return string(body(normalize(md))) }
