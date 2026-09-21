package managedprivacy

import "testing"

func TestNormalize(t *testing.T) {
	for input, want := range map[string]string{
		"":              Standard,
		"standard":      Standard,
		"metadata_only": MetadataOnly,
		"metadata-only": MetadataOnly,
		"metadata":      MetadataOnly,
	} {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("Normalize(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := Normalize("none"); err == nil {
		t.Fatal("expected invalid mode error")
	}
}
