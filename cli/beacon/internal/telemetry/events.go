package telemetry

// Every event, property and value the CLI sends is named here, so docs/cli/telemetry.mdx can be
// checked against one list and nothing reaches PostHog under a name invented at a call site.

// PostHog's own event and property names.
const (
	// eventSet updates person properties without recording a product event.
	eventSet = "$set"

	propSet                  = "$set"
	propSetOnce              = "$set_once"
	propProcessPersonProfile = "$process_person_profile"
)

// Super properties, sent with every event.
const (
	PropProduct       = "product"
	PropSurface       = "surface"
	PropBeaconVersion = "beacon_version"
	PropOS            = "os"
	PropArch          = "arch"
	PropInstallMode   = "install_mode"
	PropInstallMethod = "install_method"
	PropIsCI          = "is_ci"
	PropInvokingAgent = "invoking_agent"
)

// Super property values.
const (
	ProductBeacon = "beacon"
	SurfaceCLI    = "cli"

	InstallModeUser   = "user"
	InstallModeSystem = "system"
)

// Values of invoking_agent: the coding agent whose shell ran this command, or none.
const (
	AgentNone       = "none"
	AgentClaudeCode = "claude_code"
	AgentCursor     = "cursor"
	AgentCodex      = "codex"
	AgentGeminiCLI  = "gemini_cli"
	AgentOpenCode   = "opencode"
	AgentCline      = "cline"
)

// Person properties, set only on the person once an email is known. The email itself is never an
// event property.
const (
	PersonEmail        = "email"
	PersonHasBeacon    = "has_beacon"
	PersonBeaconUserID = "beacon_user_id"
	PersonFirstProduct = "first_product"
	PersonFirstSurface = "first_surface"
)

// GroupCompany is the group type an identified person's events are attributed to: the domain of a
// work email.
const GroupCompany = "company"
