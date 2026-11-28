package server

import "testing"

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		hdr  string
		want bool
	}{
		{"", false},
		{"deflate, br", false},
		{"gzip", true},
		{"br, gzip, deflate", true},
		{"GZIP", true},
		{"gzip;q=0", false},
		{"gzip; q=0.0", false},
		{"gzip;q=0.000", false},
		{"br, gzip;q=0.0", false},
		{"gzip;q=0.5", true},
		{"gzip;q=1.0", true},
		{"gzip;q=invalid", false},
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.hdr); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.hdr, got, tc.want)
		}
	}
}
