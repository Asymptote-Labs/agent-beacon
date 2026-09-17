package prime

import _ "embed"

// Template embeds the checked copy used by Beacon's Go installer.
// The root source of truth lives at plugins/prime-beacon/src/beacon.ts.
// Tests fail if this copy drifts.
//
//go:embed beacon.ts
var Template string
