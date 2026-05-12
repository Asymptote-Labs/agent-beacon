package version

// These variables are set at build time using ldflags
var (
	// Version is the semantic version of the CLI
	Version = "dev"
	// GitCommit is the git commit hash
	GitCommit = "unknown"
	// BuildDate is the date the binary was built
	BuildDate = "unknown"
)

// GetVersion returns the full version string
func GetVersion() string {
	return Version
}

// GetFullVersion returns the version with commit and build date
func GetFullVersion() string {
	return Version + " (" + GitCommit + ") built on " + BuildDate
}
