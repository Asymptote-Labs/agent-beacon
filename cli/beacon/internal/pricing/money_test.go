package pricing

import (
	"math"
	"testing"
)

func TestMicrodollarsFormat(t *testing.T) {
	cases := []struct {
		m      Microdollars
		places int
		want   string
	}{
		{0, 2, "0.00"},
		{321_000, 2, "0.32"},
		{321_000, 6, "0.321000"},
		{1_500_000, 0, "2"},
		{12_345, 2, "0.01"},
		{5_000, 2, "0.01"}, // half up
		{4_999, 2, "0.00"},
		{1_234_567_891, 4, "1234.5679"},
		{999_995, 5, "1.00000"}, // carry into the whole part
		{-5_000, 2, "-0.01"},    // half away from zero
		{-4_000, 2, "0.00"},     // no negative zero
		{7, 9, "0.000007"},      // places capped at 6
		{math.MaxInt64, 6, "9223372036854.775807"},
		{math.MinInt64, 6, "-9223372036854.775808"},
	}
	for _, c := range cases {
		if got := c.m.Format(c.places); got != c.want {
			t.Errorf("Microdollars(%d).Format(%d) = %q, want %q", c.m, c.places, got, c.want)
		}
	}
	if got := Microdollars(1_500_000).String(); got != "1.500000" {
		t.Errorf("String() = %q", got)
	}
	if got := Microdollars(321_000).USD(); got != 0.321 {
		t.Errorf("USD() = %v", got)
	}
}
