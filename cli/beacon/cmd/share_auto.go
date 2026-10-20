package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shareauto"
)

// `beacon share auto` turns auto-share on or off. Auto-share is server-side publishing: the hook
// adapter writes one local session.share_requested event per session, Beacon Cloud forwarding ships
// it with the rest of the runtime log, and beacon.sh creates the shared trace from the forwarded
// events and keeps it current. Hooks never upload anything, so on an endpoint that is not connected
// to Beacon Cloud the setting does nothing but write that event; enable says so.

// shareAutoCloud reports whether this endpoint forwards to Beacon Cloud (user or system mode) and
// with which privacy mode. Sharing strips content whenever the mode is Metadata-only, connected or
// not, so a mode that cannot be read is reported as Metadata-only rather than Standard, and a
// connection that cannot be confirmed as not connected. A seam for tests.
var shareAutoCloud = func() (connected bool, privacyMode string) {
	for _, userMode := range []bool{true, false} {
		if asymptote.Connected(userMode) {
			return true, enrolledPrivacyMode(userMode)
		}
	}
	return unreadableCloudConnection(asymptote.EnrollmentPath(false), endpointconfig.SystemConfigPath())
}

// enrolledPrivacyMode is the privacy mode a connected endpoint's enrollment records.
func enrolledPrivacyMode(userMode bool) string {
	enrollment, err := asymptote.LoadEnrollment(userMode)
	if err != nil {
		return managedprivacy.MetadataOnly
	}
	return recordedPrivacyMode(enrollment.PrivacyMode)
}

// recordedPrivacyMode reads a stored privacy mode. None means the record predates the setting,
// when every endpoint forwarded in Standard mode; one Beacon does not know is Metadata-only.
func recordedPrivacyMode(value string) string {
	if strings.TrimSpace(value) == "" {
		return managedprivacy.Standard
	}
	mode, err := managedprivacy.Normalize(value)
	if err != nil {
		return managedprivacy.MetadataOnly
	}
	return mode
}

// unreadableCloudConnection covers a system-mode connection the person running the command cannot
// see. The system enrollment is in a directory only root can read, so `beacon share`, `--follow`
// and the git pre-push hook, which run as the signed-in user, get a permission error where root
// would find it. The managed_ingest block of the system config.json, which everyone can read, then
// says whether the endpoint forwards and in which privacy mode. A config that cannot be read either
// (it is 0600 when it holds a HEC token) confirms no connection but still yields Metadata-only.
func unreadableCloudConnection(enrollmentPath, configPath string) (bool, string) {
	if _, err := os.Stat(enrollmentPath); !errors.Is(err, fs.ErrPermission) {
		return false, ""
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false, managedprivacy.MetadataOnly
	}
	var cfg endpointconfig.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, managedprivacy.MetadataOnly
	}
	if cfg.ManagedIngest == nil || !cfg.ManagedIngest.Enabled {
		return false, ""
	}
	return true, recordedPrivacyMode(cfg.ManagedIngest.PrivacyMode)
}

type shareAutoOptions struct {
	visibility string
	project    string
	yes        bool
	jsonOutput bool
}

var shareAutoOpts shareAutoOptions

var shareAutoCmd = &cobra.Command{
	Use:   "auto",
	Short: "Publish new sessions automatically through Beacon Cloud",
	Long: `Turn auto-share on or off, for every session or for one project.

With auto-share on, Beacon's hooks write one session.share_requested event to the local runtime log
for each new session. Beacon Cloud forwarding ships it like every other event, and beacon.sh
publishes the session as a shared trace and keeps it current as the session goes on. Hooks never
upload anything themselves: on an endpoint that is not connected to Beacon Cloud nothing is
published, and in metadata-only privacy mode the trace has no prompts, answers or output.

--project DIR applies the choice to one project: it is recorded for you, and DIR/.beacon/share.json
records it in the repository. A project file can turn auto-share off by itself, but it turns it on
only for people who ran ` + "`beacon share auto enable --project`" + ` there, so a cloned repository can never
publish your sessions.`,
	Args: cobra.NoArgs,
}

var shareAutoEnableCmd = &cobra.Command{
	Use:          "enable",
	Short:        "Turn auto-share on",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runShareAutoSet(cmd, true) },
}

var shareAutoDisableCmd = &cobra.Command{
	Use:          "disable",
	Short:        "Turn auto-share off",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runShareAutoSet(cmd, false) },
}

var shareAutoStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show whether auto-share applies here",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runShareAutoStatus,
}

func init() {
	shareCmd.AddCommand(shareAutoCmd)
	shareAutoCmd.AddCommand(shareAutoEnableCmd, shareAutoDisableCmd, shareAutoStatusCmd)
	for _, c := range []*cobra.Command{shareAutoEnableCmd, shareAutoDisableCmd, shareAutoStatusCmd} {
		c.Flags().StringVar(&shareAutoOpts.project, "project", "", "Apply to this project directory instead of every session (status: check this directory)")
		c.Flags().BoolVar(&shareAutoOpts.jsonOutput, "json", false, "Print the result as JSON")
	}
	shareAutoEnableCmd.Flags().StringVar(&shareAutoOpts.visibility, "visibility", asymptoteobserve.ShareVisibilityDirect, "Visibility of auto-shared traces: direct (anyone with the link), private or public")
	shareAutoEnableCmd.Flags().BoolVar(&shareAutoOpts.yes, "yes", false, "Enable public auto-share without asking")
}

type shareAutoStatus struct {
	SettingsPath    string `json:"settings_path"`
	Directory       string `json:"directory"`
	Enabled         bool   `json:"enabled"`
	Visibility      string `json:"visibility,omitempty"`
	Source          string `json:"source,omitempty"`
	Project         string `json:"project,omitempty"`
	UntrustedMarker string `json:"untrusted_marker,omitempty"`
	// Overridden is the project whose enabled entry is overridden here: by the off switch for
	// every session (source "user") or by the repository's opt-out file (source "marker").
	Overridden     string `json:"overridden,omitempty"`
	UserEnabled    bool   `json:"user_enabled"`
	UserState      string `json:"user_state"`
	UserVisibility string `json:"user_visibility,omitempty"`
	// Projects are the user's project entries, each with whether the user-wide off switch
	// overrides it.
	Projects         []shareAutoProject `json:"projects,omitempty"`
	CloudConnected   bool               `json:"cloud_connected"`
	CloudPrivacyMode string             `json:"cloud_privacy_mode,omitempty"`
	Warnings         []string           `json:"warnings,omitempty"`
	// GlobalOffCleared is set when enable --project cleared the user-wide off switch.
	GlobalOffCleared bool `json:"global_off_cleared,omitempty"`
}

// shareAutoProject is one project entry in the status.
type shareAutoProject struct {
	Path       string `json:"path"`
	Enabled    bool   `json:"enabled"`
	Visibility string `json:"visibility,omitempty"`
	Overridden bool   `json:"overridden,omitempty"`
}

// shareAutoUserState names the user-wide switch's state for people: "on", "off" or "not set".
func shareAutoUserState(state string) string {
	if state == shareauto.GlobalUnset {
		return "not set"
	}
	return state
}

func runShareAutoSet(cmd *cobra.Command, enable bool) error {
	err := shareAutoSet(cmd, enable)
	if err != nil && shareAutoOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"ok": false, "error": err.Error()})
	}
	return err
}

func shareAutoSet(cmd *cobra.Command, enable bool) error {
	visibility := strings.ToLower(strings.TrimSpace(shareAutoOpts.visibility))
	if enable {
		if !asymptoteobserve.ValidShareVisibility(visibility) {
			return fmt.Errorf("--visibility must be public, direct or private, not %q", shareAutoOpts.visibility)
		}
		if visibility == asymptoteobserve.ShareVisibilityPublic && !shareAutoOpts.yes {
			if err := confirmPublicAutoShare(cmd.ErrOrStderr()); err != nil {
				return err
			}
		}
	} else {
		visibility = ""
	}
	path := shareauto.DefaultPath()
	if path == "" {
		return errors.New("cannot find the home directory for the auto-share setting")
	}
	settings, err := shareauto.Load(path)
	if err != nil {
		return fmt.Errorf("read %s: %w (fix or remove it, then retry)", path, err)
	}
	project := ""
	globalOffCleared := false
	if strings.TrimSpace(shareAutoOpts.project) != "" {
		project, err = filepath.Abs(shareAutoOpts.project)
		if err != nil {
			return err
		}
		project = filepath.Clean(project)
		entry := shareauto.Project{Path: project, Enabled: enable, Visibility: visibility}
		replaced := false
		for i, p := range settings.Projects {
			if shareauto.SamePath(p.Path, project) {
				settings.Projects[i] = entry
				replaced = true
			}
		}
		if !replaced {
			settings.Projects = append(settings.Projects, entry)
		}
		// Opting a project in while the user-wide switch is off: the person wants it on here, so
		// the kill switch goes back to unset (not on: other sessions stay off).
		if enable && settings.Global() == shareauto.GlobalOff {
			settings.SetGlobal(shareauto.GlobalUnset)
			globalOffCleared = true
		}
	} else if enable {
		// Project entries are left as they are, so turning auto-share back on restores exactly
		// what was there before it was turned off.
		settings.SetGlobal(shareauto.GlobalOn)
		settings.Auto.Visibility = visibility
	} else {
		// Off is a kill switch over every project (shareauto.Resolve); the entries stay untouched.
		settings.SetGlobal(shareauto.GlobalOff)
	}
	if err := shareauto.Save(path, settings); err != nil {
		return err
	}
	if project != "" {
		if err := shareauto.SaveMarker(project, shareauto.Marker{Auto: shareauto.Auto{Enabled: enable, Visibility: visibility}}); err != nil {
			return fmt.Errorf("the setting was saved for you, but %s could not be written: %w", shareauto.MarkerPath(project), err)
		}
	}
	dir := project
	if dir == "" {
		dir, _ = filepath.Abs(".")
	}
	status := buildShareAutoStatus(path, settings, dir)
	status.GlobalOffCleared = globalOffCleared
	return printShareAutoResult(cmd, status, enable, project)
}

func confirmPublicAutoShare(errOut io.Writer) error {
	if !shareIsTTY() || isCIEnvironment() {
		return errors.New("public auto-share lists every new session in Discover for anyone to find; pass --yes to enable it without a terminal to confirm")
	}
	fmt.Fprint(errOut, "Publish every new session publicly? Anyone can find and read them in Discover. [y/N] ")
	if !confirmNo(shareStdin) {
		return errors.New("auto-share not changed")
	}
	return nil
}

func buildShareAutoStatus(path string, settings shareauto.Settings, dir string) shareAutoStatus {
	decision := shareauto.Resolve(settings, dir)
	connected, mode := shareAutoCloud()
	status := shareAutoStatus{
		SettingsPath:    path,
		Directory:       dir,
		Enabled:         decision.Enabled,
		Visibility:      decision.Visibility,
		Source:          decision.Source,
		Project:         decision.Project,
		UntrustedMarker: decision.UntrustedMarker,
		Overridden:      decision.Overridden,
		UserEnabled:     settings.Global() == shareauto.GlobalOn,
		UserState:       settings.Global(),
		UserVisibility:  settings.Auto.Visibility,
		CloudConnected:  connected,
	}
	if connected {
		status.CloudPrivacyMode = mode
	}
	for _, p := range settings.Projects {
		status.Projects = append(status.Projects, shareAutoProject{
			Path: p.Path, Enabled: p.Enabled, Visibility: p.Visibility,
			Overridden: p.Enabled && settings.Global() == shareauto.GlobalOff,
		})
	}
	if status.Enabled && status.Visibility == "" {
		status.Visibility = asymptoteobserve.ShareVisibilityDirect
	}
	if status.Enabled || status.UserEnabled {
		if !connected {
			status.Warnings = append(status.Warnings, "this endpoint is not connected to Beacon Cloud, so nothing is published: auto-share works only through Beacon Cloud forwarding (`beacon endpoint connect`); `beacon share` publishes a session by hand")
		} else if mode == managedprivacy.MetadataOnly {
			status.Warnings = append(status.Warnings, "Beacon Cloud forwarding is in metadata-only mode, so auto-shared traces have no prompts, answers, reasoning or tool output; reconnect in standard mode, or use `beacon share` for a full trace")
		}
	}
	if status.UntrustedMarker != "" && status.Source != "project" {
		status.Warnings = append(status.Warnings, fmt.Sprintf("%s asks for auto-share, which applies only after you run `beacon share auto enable --project %s`", shareauto.MarkerPath(status.UntrustedMarker), status.UntrustedMarker))
	}
	return status
}

func printShareAutoResult(cmd *cobra.Command, status shareAutoStatus, enable bool, project string) error {
	out := cmd.OutOrStdout()
	if shareAutoOpts.jsonOutput {
		return json.NewEncoder(out).Encode(map[string]any{"ok": true, "data": status})
	}
	scope := "every new session"
	if project != "" {
		scope = "new sessions in " + project
	}
	if enable {
		fmt.Fprintf(out, "Auto-share is on for %s (%s).\n", scope, status.Visibility)
		fmt.Fprintln(out, "Sessions that are already running are shared on their next event; earlier sessions are not. Use `beacon share` for those.")
		if status.GlobalOffCleared {
			fmt.Fprintln(out, "Auto-share was turned off for every session; that switch is now cleared, so projects you turned on publish again and other sessions stay off.")
		}
	} else {
		fmt.Fprintf(out, "Auto-share is off for %s. Traces already published stay published; delete them on beacon.sh.\n", scope)
		if project == "" {
			var overridden []string
			for _, p := range status.Projects {
				if p.Overridden {
					overridden = append(overridden, p.Path)
				}
			}
			if len(overridden) > 0 {
				fmt.Fprintf(out, "This overrides %d project setting(s), which are kept and apply again after `beacon share auto enable`:\n", len(overridden))
				for _, p := range overridden {
					fmt.Fprintf(out, "  %s\n", p)
				}
			}
		}
	}
	if project != "" {
		fmt.Fprintf(out, "Recorded in %s and %s.\n", status.SettingsPath, shareauto.MarkerPath(project))
	}
	for _, w := range status.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
	}
	return nil
}

func runShareAutoStatus(cmd *cobra.Command, args []string) error {
	path := shareauto.DefaultPath()
	settings, err := shareauto.Load(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	dir := strings.TrimSpace(shareAutoOpts.project)
	if dir == "" {
		dir = "."
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	status := buildShareAutoStatus(path, settings, dir)
	out := cmd.OutOrStdout()
	if shareAutoOpts.jsonOutput {
		return json.NewEncoder(out).Encode(map[string]any{"ok": true, "data": status})
	}
	state := "off"
	if status.Enabled {
		state = "on (" + status.Visibility + ")"
	}
	fmt.Fprintf(out, "Auto-share in %s: %s\n", status.Directory, state)
	switch status.Source {
	case "project":
		fmt.Fprintf(out, "  decided by your setting for the project %s\n", status.Project)
	case "marker":
		if status.Overridden != "" {
			fmt.Fprintf(out, "  disabled by the repository file %s, which overrides your setting for this project\n", shareauto.MarkerPath(status.Project))
		} else {
			fmt.Fprintf(out, "  disabled by the repository file %s\n", shareauto.MarkerPath(status.Project))
		}
	case "user":
		if status.Overridden != "" {
			fmt.Fprintf(out, "  turned off for every session, which overrides your setting for the project %s\n", status.Overridden)
		} else {
			fmt.Fprintln(out, "  decided by your setting for every session")
		}
	}
	userState := shareAutoUserState(status.UserState)
	if status.UserState == shareauto.GlobalOn && status.UserVisibility != "" {
		userState += " (" + status.UserVisibility + ")"
	}
	fmt.Fprintf(out, "Every session: %s\n", userState)
	for _, p := range status.Projects {
		state := "off"
		if p.Enabled {
			state = "on"
			if p.Visibility != "" {
				state += " (" + p.Visibility + ")"
			}
		}
		if p.Overridden {
			state += ", overridden by the off switch for every session"
		}
		fmt.Fprintf(out, "Project %s: %s\n", p.Path, state)
	}
	cloud := "not connected"
	if status.CloudConnected {
		cloud = "connected (" + managedprivacy.Label(status.CloudPrivacyMode) + ")"
	}
	fmt.Fprintf(out, "Beacon Cloud forwarding: %s\n", cloud)
	fmt.Fprintf(out, "Settings: %s\n", status.SettingsPath)
	for _, w := range status.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	return nil
}
