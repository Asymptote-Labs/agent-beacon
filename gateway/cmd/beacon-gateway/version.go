package main

import "strings"

// version is the release this source builds. Bump it before tagging: release-gateway.yml runs the
// built binary with -version and refuses a gateway-v<version> tag that disagrees.
const version = "0.2.0"

// commit and buildDate are stamped by scripts/release-build.sh with -ldflags -X; a plain go build
// leaves them empty.
var commit, buildDate string

// versionLine is what -version prints: "beacon-gateway <version>", then the commit and build date
// when the build stamped them.
func versionLine() string {
	var b strings.Builder
	b.WriteString("beacon-gateway " + version)
	if commit != "" {
		b.WriteString(" (commit " + commit)
		if buildDate != "" {
			b.WriteString(", built " + buildDate)
		}
		b.WriteString(")")
	}
	return b.String()
}
