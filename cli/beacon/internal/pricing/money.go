package pricing

import (
	"math"
	"math/big"
	"strconv"
)

// Microdollars is an amount of US dollars in millionths. Every rate and every estimate is
// integer microdollars so that summing thousands of per-request estimates cannot drift the way
// float64 cents do; conversion to a float happens once, for display.
type Microdollars int64

const microdollarsPerDollar = 1_000_000

// tokensPerRateUnit is the token count a rate is quoted for: rates are microdollars per
// million tokens.
const tokensPerRateUnit = 1_000_000

// USD returns the amount in dollars, for JSON output and arithmetic that tolerates a float.
func (m Microdollars) USD() float64 {
	return float64(m) / microdollarsPerDollar
}

// String formats the amount with all six decimal places, without a currency sign: 1.5 dollars
// is "1.500000". Exact, because it never goes through a float.
func (m Microdollars) String() string {
	return m.Format(6)
}

// Format renders the amount in dollars with the given number of decimal places (0 to 6),
// rounding half away from zero at the last place shown: Format(2) of 12_345 is "0.01", of
// 5_000 is "0.01", of 4_999 is "0.00". No currency sign, so a caller can say "~$" or "est."
// as its report requires.
func (m Microdollars) Format(places int) string {
	if places < 0 {
		places = 0
	}
	if places > 6 {
		places = 6
	}
	neg := m < 0
	// Work in uint64 so math.MinInt64 has a magnitude.
	var mag uint64
	if neg {
		mag = uint64(-(m + 1)) + 1
	} else {
		mag = uint64(m)
	}
	scale := uint64(1)
	for i := 0; i < 6-places; i++ {
		scale *= 10
	}
	if scale > 1 {
		q, r := mag/scale, mag%scale
		if r >= scale/2 {
			q++
		}
		mag = q
	}
	unit := uint64(1)
	for i := 0; i < places; i++ {
		unit *= 10
	}
	whole, frac := mag/unit, mag%unit
	s := strconv.FormatUint(whole, 10)
	if places > 0 {
		f := strconv.FormatUint(frac, 10)
		for len(f) < places {
			f = "0" + f
		}
		s += "." + f
	}
	if neg && mag != 0 {
		s = "-" + s
	}
	return s
}

// lineItem accumulates tokens x rate products exactly. A token count near the int64 limit times
// a rate in the tens of millions overflows 64 bits, so the sum is a big.Int and is divided and
// rounded once, at the end, rather than per term.
type lineItem struct {
	sum big.Int
}

func (l *lineItem) add(tokens, ratePerMTok int64) {
	if tokens <= 0 || ratePerMTok <= 0 {
		return
	}
	var t, r big.Int
	t.SetInt64(tokens)
	r.SetInt64(ratePerMTok)
	t.Mul(&t, &r)
	l.sum.Add(&l.sum, &t)
}

// total divides by the rate unit, rounding half up, and saturates at the int64 limit. The
// second result reports saturation so a caller can refuse to print a number that is not real.
func (l *lineItem) total() (Microdollars, bool) {
	var q, r big.Int
	q.QuoRem(&l.sum, big.NewInt(tokensPerRateUnit), &r)
	if r.Int64()*2 >= tokensPerRateUnit {
		q.Add(&q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return Microdollars(math.MaxInt64), true
	}
	return Microdollars(q.Int64()), false
}
