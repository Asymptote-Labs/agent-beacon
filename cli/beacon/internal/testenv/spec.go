package testenv

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

// FutureSpec is the Threat Rules spec one minor version past the one this Beacon supports, for a
// test of a rule that must be set aside. It moves with threatrules.SupportedSpec, so raising the
// supported spec rewrites no test: a literal such as threat-rules/v2.0 had been rewritten in every
// such test each time it became supported.
func FutureSpec() string {
	major, minor, _ := strings.Cut(strings.TrimPrefix(threatrules.SupportedSpec, "threat-rules/v"), ".")
	n, _ := strconv.Atoi(minor) // 0 for a spec without a minor version
	return fmt.Sprintf("threat-rules/v%s.%d", major, n+1)
}
