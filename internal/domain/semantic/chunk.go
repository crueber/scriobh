package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/text"
)

// What is embedded. The Markdown of a document goes through the AI layer's own
// rule for what may be sent (aicore.CanonText: the chosen wording, with the
// alternatives comment, ghosted text and %%private notes%% removed, and a %% or
// a ghost tag that is never closed removing the rest of its paragraph), and the
// result is made plain text by text.SearchBody, the function that makes the
// full-text index's body: one line per paragraph or heading, with markup,
// HTML comments, raw HTML and code taken out. So an embedding sees only what an
// AI request may see and only what search can see. For ordinary text the result
// is exactly doc_text.body, and offsets agree with search.query's; where a
// document has a private note that is never closed, the text is shorter than
// doc_text.body and offsets count the shorter text.
//
// Chunking. The text that is cut is that plain text.
//
// A chunk is a run of whole paragraphs of about ChunkTarget words, never more
// than ChunkMaxWords words or ChunkMaxRunes characters. A paragraph longer than
// that is cut at sentence ends, or at word ends, or (text with no spaces) at the
// character limit. The next chunk starts again from the last paragraph or two of
// the one before when they total at most ChunkOverlap words, so a passage that
// falls across a boundary is whole in one of the two.
//
// Offsets are characters into the plain text, counted the way search.query counts
// the offset of a match. A chunk's text is exactly body[Start:End]: paragraphs are
// separated by the single newline the body has, so nothing is stored but the
// offsets and a hash, and a snippet is cut from the current body and checked
// against that hash (a document that changed since is not shown from a stale
// chunk).
const (
	ChunkTarget   = 120
	ChunkMaxWords = 220
	ChunkMaxRunes = 1400
	ChunkOverlap  = 25
	// MaxChunksPerDoc bounds one document: about 70,000 words at the target size.
	MaxChunksPerDoc = 600
	// IndexVersion is bumped when the rules for what is embedded or how it is cut
	// change, so that every index built under older rules is rebuilt.
	IndexVersion = 2 // 2: footnotes referenced only from hidden text, HTML comments and front matter left out, as share links do
)

// EmbedBody is the text of a document that may be embedded: aicore.CanonText (the one rule for what may leave: it
// takes the front matter out first, before anything else, because a %% that is never closed is cut to the end of
// its paragraph, which can be a line of the front matter), then the search body, which drops the markup.
func EmbedBody(content string) string {
	return text.SearchBody(aicore.CanonText(content))
}

// Chunk is one piece of a document.
type Chunk struct {
	Start, End int // characters into the body, [Start, End)
	Text       string
	Words      int
}

// Hash is the chunk text's hash: the first 16 bytes of its SHA-256, in hex.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

type para struct {
	start, end   int // characters
	bstart, bend int // bytes
	words        int
}

// Split cuts a body into chunks. truncated is true when MaxChunksPerDoc stopped it.
func Split(body string) (chunks []Chunk, truncated bool) {
	if !utf8.ValidString(body) { // the body is made from valid text; offsets below assume it
		return nil, false
	}
	paras := paragraphs(body)
	if len(paras) == 0 {
		return nil, false
	}
	i := 0
	for i < len(paras) {
		if len(chunks) >= MaxChunksPerDoc {
			return chunks, true
		}
		j, words, runes := i, 0, 0
		for j < len(paras) {
			u := paras[j]
			ur := u.end - u.start
			if j > i && (words+u.words > ChunkMaxWords || runes+1+ur > ChunkMaxRunes) {
				break
			}
			words += u.words
			runes += ur + 1
			j++
			if words >= ChunkTarget {
				break
			}
		}
		chunks = append(chunks, makeChunk(body, paras[i], paras[j-1]))
		if j >= len(paras) {
			break
		}
		// The next chunk begins again from trailing paragraphs that fit the overlap.
		next, ow := j, 0
		for k := j - 1; k > i; k-- {
			if ow+paras[k].words > ChunkOverlap {
				break
			}
			ow += paras[k].words
			next = k
		}
		i = next
	}
	return chunks, false
}

func makeChunk(body string, first, last para) Chunk {
	text := body[first.bstart:last.bend]
	return Chunk{Start: first.start, End: last.end, Text: text, Words: countWords(text)}
}

// runeSlice is body[a:b] counted in characters. It walks from the start each
// time; callers use it for a few chunks at a time or build offsets themselves.
func runeSlice(s string, a, b int) string {
	bs, be, n := -1, len(s), 0
	for i := range s {
		if n == a {
			bs = i
		}
		if n == b {
			be = i
			break
		}
		n++
	}
	if bs < 0 {
		return ""
	}
	return s[bs:be]
}

func countWords(s string) int { return len(strings.Fields(s)) }

// paragraphs splits the body into paras, one per non-empty line, with character
// offsets. A line over the limits is cut into several paras.
func paragraphs(body string) []para {
	var out []para
	pos, bpos := 0, 0 // characters and bytes
	for len(body) > 0 {
		line := body
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			line, body = body[:i], body[i+1:]
		} else {
			body = ""
		}
		n := utf8.RuneCountInString(line)
		if strings.TrimSpace(line) != "" {
			out = append(out, cutLine(line, pos, bpos)...)
		}
		pos += n + 1
		bpos += len(line) + 1
	}
	return out
}

// cutLine returns the line as one unit, or as several when it is too long.
// base and bbase are where the line starts, in characters and in bytes.
func cutLine(line string, base, bbase int) []para {
	w := countWords(line)
	r := utf8.RuneCountInString(line)
	if w <= ChunkMaxWords && r <= ChunkMaxRunes {
		return []para{{start: base, end: base + r, bstart: bbase, bend: bbase + len(line), words: w}}
	}
	runes := []rune(line)
	rb := make([]int, len(runes)+1) // byte offset of each character
	for i, c := range runes {
		rb[i+1] = rb[i] + utf8.RuneLen(c)
	}
	var out []para
	for from := 0; from < len(runes); {
		to := pieceEnd(runes, from)
		// Trim space from the piece's span, so the offsets are those of its text.
		a, b := from, to
		for a < b && unicode.IsSpace(runes[a]) {
			a++
		}
		for b > a && unicode.IsSpace(runes[b-1]) {
			b--
		}
		if a < b {
			out = append(out, para{start: base + a, end: base + b, bstart: bbase + rb[a], bend: bbase + rb[b], words: countWords(string(runes[a:b]))})
		}
		from = to
	}
	return out
}

// pieceEnd is where a piece of a long line that starts at from ends: the first
// sentence end after the target number of words, else the last sentence end
// within the limits, else the last space, else the character limit.
func pieceEnd(rs []rune, from int) int {
	limit := min(from+ChunkMaxRunes, len(rs))
	if limit == len(rs) && countWords(string(rs[from:])) <= ChunkMaxWords {
		return len(rs)
	}
	words, inWord := 0, false
	lastSentence, lastSpace := -1, -1
	for i := from; i < limit; i++ {
		r := rs[i]
		if unicode.IsSpace(r) {
			if inWord {
				words++
			}
			inWord = false
			lastSpace = i + 1
			if words >= ChunkMaxWords {
				break
			}
			continue
		}
		inWord = true
		if isSentenceEnd(r) && (i+1 >= len(rs) || unicode.IsSpace(rs[i+1]) || r >= 0x3000) {
			lastSentence = i + 1
			if words >= ChunkTarget {
				return lastSentence
			}
		}
	}
	switch {
	case lastSentence > from && lastSentence-from >= ChunkMaxRunes/8:
		return lastSentence
	case lastSpace > from:
		return lastSpace
	}
	return limit
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '…', '。', '！', '？':
		return true
	}
	return false
}
