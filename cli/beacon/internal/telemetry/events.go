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

// Install events, sent by `beacon endpoint install` (not by a dry run).
const (
	// EventInstallStarted is sent once the install is past its dry-run return, before the wizard.
	EventInstallStarted = "cli_install_started"
	// EventInstallCompleted is sent when the install finishes successfully, after hooks, skills,
	// the session backfill and any Beacon Cloud connect.
	EventInstallCompleted = "cli_install_completed"
)

// Install event properties.
const (
	// PropSetupPath is how the install is being driven, one of the SetupPath values.
	PropSetupPath = "setup_path"
	// PropDetectedHarnesses lists the agent runtimes discovery found, by their canonical names.
	PropDetectedHarnesses = "detected_harnesses"
	// PropIsFirstForDevice is true when this machine had never finished onboarding before the
	// install started.
	PropIsFirstForDevice = "is_first_for_device"
	// PropDestination is where the endpoint's agent telemetry goes, a Destination value.
	PropDestination = "destination"
	// PropHarnessesConfigured lists the install targets (OTLP and hooks) the install configured.
	PropHarnessesConfigured = "harnesses_configured"
	// PropBackfillSessionCount is how many sessions the install's session backfill wrote records
	// for, 0 when the backfill was off.
	PropBackfillSessionCount = "backfill_session_count"
	// PropDurationMS is the install's wall time in milliseconds.
	PropDurationMS = "duration_ms"
)

// Values of setup_path.
const (
	// SetupPathPackage is the system package's install script (BEACON_INSTALL_SOURCE=package).
	SetupPathPackage = "package"
	// SetupPathCI is a CI job.
	SetupPathCI = "ci"
	// SetupPathWizard is an interactive install that shows the onboarding wizard.
	SetupPathWizard = "wizard"
	// SetupPathAgent is a coding agent running the install without a terminal.
	SetupPathAgent = "agent"
	// SetupPathNoninteractive is everything else: scripts, MDM, a terminal past onboarding.
	SetupPathNoninteractive = "noninteractive"
)

// Values of destination.
const (
	DestinationLocal = "local"
	DestinationCloud = "cloud"
)

// Identity events. Each is followed by Identify, so the person it names is known from then on.
const (
	// EventSignInCompleted is sent when a Beacon sign-in succeeds.
	EventSignInCompleted = "cli_sign_in_completed"
	// EventEmailSubmitted is sent when a person gives an email without signing in.
	EventEmailSubmitted = "cli_email_submitted"
)

// Identity event properties.
const (
	// PropTrigger is what started a sign-in, a Trigger value.
	PropTrigger = "trigger"
	// PropEmailSource is where a submitted email came from, an EmailSource value.
	PropEmailSource = "email_source"
	// PropEmailDomainKind says whether a submitted email is at a work or a personal domain, never
	// the domain itself.
	PropEmailDomainKind = "email_domain_kind"
)

// Values of trigger.
const (
	TriggerWizard       = "wizard"
	TriggerLoginCommand = "login_command"
)

// Values of email_source.
const (
	EmailSourceFlag   = "flag"
	EmailSourceEnv    = "env"
	EmailSourcePrompt = "prompt"
)

// Values of email_domain_kind.
const (
	EmailDomainWork = "work"
	EmailDomainFree = "free"
)
