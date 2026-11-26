package imports

import (
	"crypto/sha256"
	"sync"

	"github.com/crueber/scriobh/internal/text"
)

// A draft is previewed again for every change, and a change leaves most of the book as it was: a chapter that was not cut has the same
// text, and the same word count. The counts of recent texts are kept, keyed by the SHA-256 of the text, so no words of the book are held
// for them, and let go when a draft ends (dropCaches). text.WordCount is a function of its input alone, so a hit is the number it would
// have counted.
const wordMemoEntries = 8192

var wordMemo = struct {
	sync.Mutex
	m map[[sha256.Size]byte]int
}{m: map[[sha256.Size]byte]int{}}

// wordCount is text.WordCount of a body, from the memory of the counts made for the same text.
func wordCount(body string) int {
	key := sha256.Sum256([]byte(body))
	wordMemo.Lock()
	n, ok := wordMemo.m[key]
	wordMemo.Unlock()
	if ok {
		return n
	}
	n = text.WordCount(body)
	wordMemo.Lock()
	defer wordMemo.Unlock()
	if len(wordMemo.m) >= wordMemoEntries {
		wordMemo.m = map[[sha256.Size]byte]int{}
	}
	wordMemo.m[key] = n
	return n
}

// forgetWordCounts lets go of every count held (a draft has ended).
func forgetWordCounts() {
	wordMemo.Lock()
	wordMemo.m = map[[sha256.Size]byte]int{}
	wordMemo.Unlock()
}
