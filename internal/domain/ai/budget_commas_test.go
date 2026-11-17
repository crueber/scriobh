package ai

import "testing"

func TestCommasWritesThousandsSeparators(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 2180: "2,180", 1234567: "1,234,567", -12345: "-12,345"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}
