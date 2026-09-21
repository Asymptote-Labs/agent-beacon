package managedprivacy

import (
	"fmt"
	"strings"
)

const (
	Standard     = "standard"
	MetadataOnly = "metadata_only"
)

var Modes = []string{Standard, MetadataOnly}

func Normalize(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", Standard:
		return Standard, nil
	case MetadataOnly, "metadata-only", "metadata":
		return MetadataOnly, nil
	default:
		return "", fmt.Errorf("privacy mode must be %q or %q", Standard, MetadataOnly)
	}
}

func Label(value string) string {
	if value == MetadataOnly {
		return "Metadata only"
	}
	return "Standard"
}
