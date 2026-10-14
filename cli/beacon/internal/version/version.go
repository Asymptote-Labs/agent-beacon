package version

// DefaultEdition is the edition of every build made from this repository.
const DefaultEdition = "community"

// These variables are set at build time using ldflags
var (
	// Version is the semantic version of the CLI
	Version = "dev"
	// GitCommit is the git commit hash
	GitCommit = "unknown"
	// BuildDate is the date the binary was built
	BuildDate = "unknown"
	// Edition names the build. A build that compiles in files of its own sets it at link time
	// (-X .../internal/version.Edition=<name>), and `beacon version`, endpoint status and the
	// inventory report it so a fleet can tell its builds apart.
	Edition = DefaultEdition
)

// GetVersion returns the full version string
func GetVersion() string {
	return Version
}

// GetFullVersion returns the version with commit and build date, and the edition when it is not
// the default. The edition goes last: the self-updater's health check reads the word right after
// "version" in `beacon version` output and compares it with the version it just installed.
func GetFullVersion() string {
	full := Version + " (" + GitCommit + ") built on " + BuildDate
	if edition := ReportedEdition(); edition != "" {
		full += ", " + edition + " edition"
	}
	return full
}

// ReportedEdition returns the edition to report, or "" for the default edition, so builds from
// this repository report nothing new.
func ReportedEdition() string {
	if Edition == "" || Edition == DefaultEdition {
		return ""
	}
	return Edition
}
