package onboarding

import "strings"

// ciEnvMarkers are the variables CI systems conventionally set.
var ciEnvMarkers = []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "JENKINS_URL", "TEAMCITY_VERSION"}

// IsCIEnvironment reports whether the environment is a CI job. The onboarding gate skips the
// wizard there, and telemetry reports it as is_ci.
func IsCIEnvironment(getenv func(string) string) bool {
	for _, key := range ciEnvMarkers {
		if strings.TrimSpace(getenv(key)) != "" {
			return true
		}
	}
	return false
}
