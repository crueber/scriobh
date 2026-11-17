package ai_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
)

// What a provider says passes one filter before anything sees it, so accepting a suggestion into the
// manuscript cannot bring in characters that hide or reorder text.
func TestOutputIsFilteredOfInvisibleAndSpoofingCharacters(t *testing.T) {
	nasty := "Line one\tTabbed\r\nLine two \x1b[31mred\x1b[0m \x07bell \x7fdel \u0085next \u009fend " +
		"zero​width joiner‍here mark‎‏ ‮evil‬ iso⁦x⁩ bom\uFEFFend \x00nul " +
		"café ñ 日本語 and emoji 😀"
	want := "Line one\tTabbed\nLine two [31mred[0m bell del next end zerowidth joiner‍here mark evil isox bomend nul café ñ 日本語 and emoji 😀"
	e := newEnv(t, nil)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) { oaiReply(w, r, b, nasty) })
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	out, err := e.run(e.editor, e.freeform("Hello"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != want {
		t.Errorf("whole answer:\n got %q\nwant %q", out.Text, want)
	}
	// The same through a stream, where it arrives in pieces: what the browser is sent and the final text agree.
	in := e.freeform("Hello")
	in.Stream = true
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	sink := &collect{}
	res, err2 := e.svc.Run(aicore.WithStream(context.Background(), sink), e.d, e.editor, in)
	if err2 != nil {
		t.Fatal(err2)
	}
	if res.Text != want || sink.text.String() != want {
		t.Errorf("stream:\n final %q\n sent  %q\n want  %q", res.Text, sink.text.String(), want)
	}
	for _, r := range sink.text.String() + res.Text {
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f || r == 0xFEFF || r >= 0x202A && r <= 0x202E || r >= 0x200B && r <= 0x200F && r != 0x200C && r != 0x200D || r >= 0x2066 && r <= 0x2069 {
			t.Errorf("U+%04X got through", r)
		}
	}
	if !strings.Contains(res.Text, "\t") || !strings.Contains(res.Text, "\n") {
		t.Error("tab and newline must survive")
	}
}
