package asymptoteobserve

import "strings"

// Provenance: how Beacon came to know a thing, recorded alongside the thing itself.
//
// Beacon collects from runtimes over four different mechanisms whose fidelity differs by roughly
// an order of magnitude. A Claude Code `PermissionRequest` hook hands Beacon a typed payload that
// names the operation. A Codex OTLP log record hands it a body string that Beacon has to classify
// by substring match, with a catch-all that lands on `tool.invoked` when nothing matches. Both
// produced an `event.action` in the same field, and until these two markers existed nothing in the
// event distinguished them -- so every rule in rules/, every SIEM query, and every dashboard
// treated a reported fact and a keyword guess as the same claim.
//
// Two independent axes, hence two fields rather than one:
//
//   - harness.collection_method is about the pipe: which mechanism carried this event off the
//     runtime. Stable for the lifetime of an install, useful for "what is my coverage" questions.
//   - event.fidelity is about the specific claim: did the source name this action, or did Beacon
//     derive it. Varies event to event within one collection method, because an OTLP stream
//     carries both records that declare `event.name` and records that carry only prose.
//
// Neither field is required. An event Beacon emits about itself (a health heartbeat, a self-update
// result) has no collecting harness and no source action to be faithful to, and leaves both empty
// rather than asserting a method it did not use.
const (
	// CollectionMethodHook is a runtime-native hook: the runtime executes `beacon-hooks` and
	// hands it a structured payload for a named lifecycle event.
	CollectionMethodHook = "hook"
	// CollectionMethodOTLP is OpenTelemetry data the runtime exported to Beacon's local
	// collector, converted by the beaconjson exporter.
	CollectionMethodOTLP = "otlp"
	// CollectionMethodPlugin is a Beacon-managed plugin or extension file the runtime loads,
	// which calls `beacon-hooks` on the runtime's own event callbacks. Distinct from `hook`
	// because Beacon ships and versions the plugin source, so its coverage is Beacon's to fix
	// rather than the vendor's to expose.
	CollectionMethodPlugin = "plugin"
	// CollectionMethodPoll is a periodic pull from a runtime's API, where Beacon sees whatever
	// the API returns after the fact rather than observing the agent as it works.
	CollectionMethodPoll = "poll"
)

const (
	// FidelityObserved means the source named this action. The value came from a hook event
	// name, a plugin event type, an explicit `event.action` attribute, or a structured
	// attribute whose value identifies the operation (`mcp.method.name`, a known
	// `claude_code.*`/Codex `event.name`, a tool-call id). A consumer can treat the action as
	// a report.
	FidelityObserved = "observed"
	// FidelityInferred means Beacon derived this action rather than being told it: a substring
	// match over free text, a catch-all default, or an action synthesized from an adjacent
	// observation. The action is Beacon's best reading and may be wrong. A consumer that needs
	// certainty -- counting approvals, alerting on a specific action -- should exclude these.
	FidelityInferred = "inferred"
)

// ValidCollectionMethod reports whether value is one of the four defined methods. The empty
// string is not a method and is rejected here; callers that allow "unset" check for it first,
// which is what Validate does.
func ValidCollectionMethod(value string) bool {
	switch value {
	case CollectionMethodHook, CollectionMethodOTLP, CollectionMethodPlugin, CollectionMethodPoll:
		return true
	}
	return false
}

// ValidFidelity reports whether value is one of the two defined fidelities, on the same terms as
// ValidCollectionMethod.
func ValidFidelity(value string) bool {
	switch value {
	case FidelityObserved, FidelityInferred:
		return true
	}
	return false
}

// CollectionMethodForPlatform maps a `beacon-hooks --platform` value onto the mechanism that
// platform actually uses.
//
// It lives here, beside NormalizeHarnessName, for the same reason that function does: the hook
// adapter writes this field from its `--platform` flag and no other code path can see that flag,
// so a mapping kept anywhere else would be a second definition waiting to disagree. Keying on
// `--platform` rather than on the normalized harness name is deliberate -- the flag records which
// integration shape Beacon installed, which is exactly the question, while the harness name
// records which product it was, which is not. VS Code is the case that separates them: hook and
// OTLP telemetry for it normalize to the same `vscode_copilot` harness, but only one of the two
// arrives through a hook.
//
// Platforms that use a Beacon-managed plugin or extension file are enumerated rather than
// defaulted, because the default has to be something and `hook` is the safer wrong answer: a
// plugin platform reported as `hook` overstates vendor support, while a hook platform reported as
// `plugin` would send someone to fix Beacon's source for a payload the vendor never sent. New
// plugin-shaped integrations belong in the list; new hook-shaped ones need no change.
func CollectionMethodForPlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "":
		return ""
	case "opencode", "cline", "pi", "prime":
		return CollectionMethodPlugin
	default:
		return CollectionMethodHook
	}
}
