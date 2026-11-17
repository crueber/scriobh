package ai

// Token and cost estimates. The estimate is deliberately simple and a little
// high, because it reserves budget before a call and shows a number before the
// person approves it; the provider's own count replaces it after the run.
//
//   - an ASCII character counts a quarter of a token (four characters a token),
//   - another character below U+2E80 (accented Latin, Cyrillic, Greek) a half,
//   - a CJK or other wide character one token,
//   - a character outside the basic plane (most emoji) two,
//   - each message adds 4 tokens of framing and the request 3.
//
// The result is rounded up. English prose lands within about 20 percent of a
// real tokenizer; text in other scripts is estimated high on purpose.

// EstimateTokens estimates the tokens of one text.
func EstimateTokens(s string) int {
	q := 0 // quarter tokens
	for _, r := range s {
		switch {
		case r < 0x80:
			q++
		case r < 0x2E80:
			q += 2
		case r < 0x10000:
			q += 4
		default:
			q += 8
		}
	}
	return (q + 3) / 4
}

// EstimatePayload estimates the input tokens of a payload.
func EstimatePayload(p Payload) int {
	n := 3
	for _, m := range p.Messages {
		n += 4 + EstimateTokens(m.Content)
	}
	return n
}

// Price is a model's price in micro-units of the instance currency per million
// tokens.
type Price struct {
	InputMicros  int64
	OutputMicros int64
}

// Cost is the cost of a call in micro-units, rounded up. Counts are clamped so
// a corrupt count cannot overflow.
func (p Price) Cost(in, out int) int64 {
	cl := func(n int) int64 {
		if n < 0 {
			return 0
		}
		if n > 100_000_000 {
			return 100_000_000
		}
		return int64(n)
	}
	up := func(tokens, per int64) int64 {
		if per < 0 {
			per = 0
		}
		if per > 1_000_000_000_000 { // a million units per million tokens: not a price
			per = 1_000_000_000_000
		}
		return (tokens*per + 999_999) / 1_000_000
	}
	return up(cl(in), p.InputMicros) + up(cl(out), p.OutputMicros)
}
