package pricing

import "testing"

func TestCacheWriteFallbackDescribesOnlyRatesUsed(t *testing.T) {
	rates := Rates{RateSet: RateSet{Input: 2_000_000, Output: 3_000_000, CacheWrite1h: 5_000_000}}
	for _, tc := range []struct {
		name           string
		total, oneHour int64
		wantCost       Microdollars
		wantFallback   bool
	}{
		{"only_one_hour", 1000, 1000, 5000, false},
		{"mixed_ttls", 1000, 600, 3800, true},
		{"only_five_minutes", 1000, 0, 2000, true},
		{"clamped_one_hour", 1000, 2000, 5000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Cost(rates, Tokens{CacheCreation: tc.total, CacheCreation1h: tc.oneHour}, false)
			if got.Cost != tc.wantCost || got.CacheWriteAtInputRate != tc.wantFallback || got.CacheWrite1hAtWriteRate {
				t.Fatalf("estimate = %+v; want cost %d input fallback %v and no 1h fallback", got, tc.wantCost, tc.wantFallback)
			}
		})
	}
}

func TestOneHourOnlyOverrideDoesNotReportInputFallback(t *testing.T) {
	overrides, err := LoadOverrides([]byte(`{"schema":"beacon.pricing.overrides/v1","models":{"internal-model":{"input_usd_per_mtok":2,"output_usd_per_mtok":3,"cache_write_1h_usd_per_mtok":5}}}`), overridesCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	rates, ok := overrides.Rates("internal-model")
	if !ok {
		t.Fatal("override model missing")
	}
	got := Cost(rates, Tokens{CacheCreation: 1000, CacheCreation1h: 1000}, false)
	if got.Cost != 5000 || got.CacheWriteAtInputRate || got.CacheWrite1hAtWriteRate {
		t.Fatalf("override estimate = %+v; want explicit one-hour rate without fallback", got)
	}
}
