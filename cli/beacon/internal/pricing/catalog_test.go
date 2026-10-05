package pricing

import (
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedCatalogLoads(t *testing.T) {
	c, err := LoadCatalog(embeddedCatalog)
	if err != nil {
		t.Fatalf("embedded catalog.json: %v", err)
	}
	if Default() == nil {
		t.Fatal("Default() is nil")
	}
	src := c.Source()
	if src.License != "MIT" || !strings.Contains(src.Name, "LiteLLM") {
		t.Errorf("source must credit LiteLLM (MIT): %+v", src)
	}
	if !strings.HasPrefix(src.URL, "https://raw.githubusercontent.com/BerriAI/litellm/") {
		t.Errorf("source URL = %q", src.URL)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(src.Commit) {
		t.Errorf("source commit = %q, want the full upstream commit the catalog was generated from", src.Commit)
	}
	date := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	if !date.MatchString(src.FetchedAt) || !date.MatchString(c.GeneratedAt()) {
		t.Errorf("dates fetched_at=%q generated_at=%q", src.FetchedAt, c.GeneratedAt())
	}
	// The catalog is embedded in the CLI; keep it small.
	if n := len(embeddedCatalog); n > 300_000 {
		t.Errorf("catalog.json is %d bytes; keep it well under 300KB", n)
	}
	if n := len(c.Keys()); n < 200 {
		t.Errorf("catalog has only %d models", n)
	}
}

// LoadCatalog enforces these too; the test states them independently so a loosened validator
// cannot quietly let a bad catalog through.
func TestEmbeddedCatalogIntegrity(t *testing.T) {
	c := Default()
	for _, key := range c.Keys() {
		r, ok := c.Rates(key)
		if !ok {
			t.Fatalf("%s listed but has no rates", key)
		}
		if r.Provider == "" {
			t.Errorf("%s: no provider", key)
		}
		check := func(where string, s RateSet) {
			if s.Input <= 0 || s.Output <= 0 {
				t.Errorf("%s %s: input %d output %d, both must be positive", key, where, s.Input, s.Output)
			}
			if s.CacheRead < 0 || s.CacheWrite < 0 || s.CacheWrite1h < 0 {
				t.Errorf("%s %s: negative cache rate %+v", key, where, s)
			}
		}
		check("base", r.RateSet)
		var prev int64
		for _, b := range r.Bands {
			if b.AboveTokens <= prev {
				t.Errorf("%s: band thresholds not strictly increasing (%d after %d)", key, b.AboveTokens, prev)
			}
			prev = b.AboveTokens
			check("band", b.RateSet)
			// Bands are complete tuples: a rate the base publishes is never missing above the
			// threshold.
			if (r.CacheRead > 0 && b.CacheRead == 0) || (r.CacheWrite > 0 && b.CacheWrite == 0) || (r.CacheWrite1h > 0 && b.CacheWrite1h == 0) {
				t.Errorf("%s: band above %d drops a base cache rate: base %+v band %+v", key, b.AboveTokens, r.RateSet, b.RateSet)
			}
		}
	}
}

func TestLoadCatalogRejectsBadInput(t *testing.T) {
	good := `{"schema_version":1,"source":{"name":"x","license":"MIT","url":"u","fetched_at":"2026-01-01"},"generated_at":"2026-01-01","unit":"microdollars per million tokens","models":{"m":%s}}`
	for name, entry := range map[string]string{
		"zero input":        `{"provider":"p","input":0,"output":1}`,
		"negative cache":    `{"provider":"p","input":1,"output":1,"cache_read":-1}`,
		"unsorted bands":    `{"provider":"p","input":1,"output":1,"bands":[{"above_tokens":200000,"input":2,"output":2},{"above_tokens":100000,"input":3,"output":3}]}`,
		"duplicate band":    `{"provider":"p","input":1,"output":1,"bands":[{"above_tokens":200000,"input":2,"output":2},{"above_tokens":200000,"input":3,"output":3}]}`,
		"band without rate": `{"provider":"p","input":1,"output":1,"bands":[{"above_tokens":200000}]}`,
	} {
		if _, err := LoadCatalog([]byte(strings.Replace(good, "%s", entry, 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := LoadCatalog([]byte(strings.Replace(good, "%s", `{"provider":"p","input":1,"output":1}`, 1))); err != nil {
		t.Errorf("valid catalog rejected: %v", err)
	}
	if _, err := LoadCatalog([]byte(strings.Replace(strings.Replace(good, "%s", `{"provider":"p","input":1,"output":1}`, 1), `"schema_version":1`, `"schema_version":2`, 1))); err == nil {
		t.Error("future schema_version accepted")
	}
}
