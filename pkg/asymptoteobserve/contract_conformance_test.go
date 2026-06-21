package asymptoteobserve

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// tsConstantsPath is the TypeScript SDK constants file, resolved relative to this
// package directory (repo root is two levels up from pkg/asymptoteobserve).
var tsConstantsPath = filepath.Join("..", "..", "packages", "asymptote-sdk-js", "src", "constants.ts")

// tsExportConst matches `export const NAME = "VALUE";` declarations.
var tsExportConst = regexp.MustCompile(`(?m)^export const (\w+) = "([^"]*)";`)

// TestContractConstantsInSyncWithTypeScriptSDK pins the Beacon contract constants that
// are duplicated across the Go endpoint schema and the TypeScript SDK. CLAUDE.md treats
// vendor/product/schema_version as release contracts and the beacon.* attribute names as
// stable identifiers; this guard fails CI if the two copies ever drift apart.
//
// Coverage today is limited to the constants that both sides define explicitly. As the Go
// converter's bare attribute-name literals are promoted to a shared const block (a later
// cleanup PR), extend `want` to cover them too.
func TestContractConstantsInSyncWithTypeScriptSDK(t *testing.T) {
	tsConsts := readTSConstants(t)

	// Go constant value -> TypeScript export name expected to match it.
	want := map[string]struct {
		goName, goValue, tsName string
	}{
		"vendor":         {"Vendor", Vendor, "BEACON_VENDOR"},
		"product":        {"Product", Product, "BEACON_PRODUCT"},
		"schema_version": {"SchemaVersion", SchemaVersion, "BEACON_SCHEMA_VERSION"},
		"origin_attr":    {"AttributeOrigin", AttributeOrigin, "ATTR_BEACON_ORIGIN"},
	}

	for _, c := range want {
		tsValue, ok := tsConsts[c.tsName]
		if !ok {
			t.Errorf("TypeScript SDK is missing export const %s in %s; it must mirror Go %s = %q",
				c.tsName, tsConstantsPath, c.goName, c.goValue)
			continue
		}
		if tsValue != c.goValue {
			t.Errorf("contract drift: Go %s = %q but TypeScript %s = %q.\n"+
				"Keep %s (event.go) and %s (constants.ts) in sync.",
				c.goName, c.goValue, c.tsName, tsValue, c.goName, c.tsName)
		}
	}
}

func readTSConstants(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(tsConstantsPath)
	if err != nil {
		t.Fatalf("read TypeScript SDK constants at %s: %v", tsConstantsPath, err)
	}
	consts := make(map[string]string)
	for _, m := range tsExportConst.FindAllStringSubmatch(string(data), -1) {
		consts[m[1]] = m[2]
	}
	if len(consts) == 0 {
		t.Fatalf("no `export const NAME = \"...\"` declarations parsed from %s", tsConstantsPath)
	}
	return consts
}
