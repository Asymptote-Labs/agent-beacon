// Package telemetry sends the CLI's product usage events to PostHog.
//
// It is the only place the CLI talks to PostHog, and it is built so that it can never cost a
// command anything: events are queued in memory and sent in the background, Close waits at most
// FlushTimeout on exit, and every error is swallowed. Development builds, BEACON_TELEMETRY=0 and
// DO_NOT_TRACK send nothing. The hook adapter and the collector do not link it.
//
// Events carry counts, names from fixed lists and the super properties, never session content,
// prompts, file paths, repository names, hostnames or error text. An email is sent only as a
// person property, after the person signs in or gives it.
package telemetry

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/posthog/posthog-go"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

const (
	// DefaultEndpoint is beacon.sh's reverse proxy to PostHog.
	DefaultEndpoint = "https://beacon.sh/ingest"
	// projectToken is the public PostHog project token the beacon.sh website also uses.
	projectToken = "phc_mAC6ywfI3poAo9dBeuNJUJySdxYdJzvoYapN5Wn9cVN"
	// FlushTimeout caps how long Close waits for queued events on exit.
	FlushTimeout = 1500 * time.Millisecond
)

// Notice is printed to stderr the first time telemetry is on for a home directory.
const Notice = "Beacon sends CLI usage data (never prompts, code or file paths). Turn it off with BEACON_TELEMETRY=0. Learn more: https://docs.beacon.sh/cli/telemetry"

// Seams for tests.
var (
	endpoint     = DefaultEndpoint
	releaseBuild = updatecheck.CanCheckVersion
)

// Options describe the running command to Init.
type Options struct {
	// Version is the CLI version; a development build ("dev") sends nothing.
	Version string
	// InstallMode is InstallModeUser or InstallModeSystem.
	InstallMode string
	// Getenv and Environ default to the process environment.
	Getenv  func(string) string
	Environ func() []string
	// Stderr receives the first-run notice.
	Stderr io.Writer
	Now    func() time.Time
}

// recorder is the enabled state of one process.
type recorder struct {
	mu         sync.Mutex
	home       string
	persist    bool
	installID  string
	distinctID string
	identified bool
	company    string
	supers     posthog.Properties
	client     posthog.Client
}

var (
	mu      sync.Mutex
	current *recorder
)

// Init turns telemetry on for this process unless it is a development build or the environment
// opts out. It makes sure the machine's install ID exists in profile.json, prints the first-run
// notice once, and never touches the network: the PostHog client starts with the first event.
func Init(opts Options) {
	r := newRecorder(opts)
	mu.Lock()
	previous := current
	current = r
	mu.Unlock()
	previous.close()
}

// Enabled reports whether Init turned telemetry on.
func Enabled() bool {
	return active() != nil
}

func newRecorder(opts Options) *recorder {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if !releaseBuild(opts.Version) || OptedOut(getenv) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		// A scheduled job without HOME would resolve profile.json against its working directory.
		return nil
	}
	persist := homeWritable(home)

	profile := onboarding.Load()
	if profile.InstallID == "" {
		if _, err := onboarding.EnsureInstallID(&profile); err != nil {
			return nil
		}
		// Onboarding mints the ID too, but saves it only when it records an answer, so an install
		// that never ran the wizard would get a new anonymous ID on every run.
		if persist {
			_ = onboarding.Save(profile)
		}
	}

	st := loadState(home)
	if st.NoticeShownAt == "" && opts.Stderr != nil {
		fmt.Fprintln(opts.Stderr, Notice)
		st.NoticeShownAt = now().UTC().Format(time.RFC3339)
		if persist {
			_ = saveState(home, st)
		}
	}

	installMode := opts.InstallMode
	if installMode != InstallModeSystem {
		installMode = InstallModeUser
	}
	r := &recorder{
		home:       home,
		persist:    persist,
		installID:  profile.InstallID,
		distinctID: profile.InstallID,
		supers: posthog.Properties{
			PropProduct:       ProductBeacon,
			PropSurface:       SurfaceCLI,
			PropBeaconVersion: opts.Version,
			PropOS:            runtime.GOOS,
			PropArch:          runtime.GOARCH,
			PropInstallMode:   installMode,
			PropInstallMethod: onboarding.InstallMethod(),
			PropIsCI:          onboarding.IsCIEnvironment(getenv),
			PropInvokingAgent: InvokingAgent(environ()),
		},
	}
	if st.PHDistinctID != "" {
		r.distinctID = st.PHDistinctID
		r.identified = true
		r.company = st.Company
	}
	return r
}

func active() *recorder {
	mu.Lock()
	defer mu.Unlock()
	return current
}

// SuperProperty returns one super property of the running process, or nil when telemetry is off,
// so a caller can branch on what every event already says (invoking_agent, is_ci).
func SuperProperty(name string) any {
	r := active()
	if r == nil {
		return nil
	}
	return r.supers[name]
}

// Capture queues one event. props must hold only values from events.go's lists, counts, durations
// and booleans.
func Capture(event string, props map[string]any) {
	r := active()
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	properties := posthog.NewProperties()
	for key, value := range props {
		properties.Set(key, value)
	}
	msg := posthog.Capture{DistinctId: r.distinctID, Event: event, Properties: properties}
	if r.identified {
		msg.Groups = r.groups()
	} else {
		// Until an email is known the install ID is not a person, and PostHog should not make one.
		properties.Set(propProcessPersonProfile, false)
	}
	r.enqueue(msg)
}

// Identify ties this machine to the person with email: it aliases the install ID to the email's
// distinct ID, sets the person properties, and sends every later event, in this run and the next
// ones, as that person. userID is the Beacon account ID when the person signed in, else "".
func Identify(email, userID string) {
	r := active()
	if r == nil {
		return
	}
	email = normalizeEmail(email)
	if !strings.Contains(email, "@") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := DistinctIDForEmail(email)
	if r.distinctID != id {
		r.enqueue(posthog.Alias{DistinctId: id, Alias: r.installID})
	}
	set := posthog.Properties{PersonEmail: email, PersonHasBeacon: true}
	if userID = strings.TrimSpace(userID); userID != "" {
		set[PersonBeaconUserID] = userID
	}
	r.distinctID = id
	r.identified = true
	r.company = CompanyDomain(email)
	r.enqueue(posthog.Capture{
		DistinctId: id,
		Event:      eventSet,
		Properties: posthog.Properties{
			propSet:     set,
			propSetOnce: posthog.Properties{PersonFirstProduct: ProductBeacon, PersonFirstSurface: SurfaceCLI},
		},
		Groups: r.groups(),
	})
	if r.persist {
		st := loadState(r.home)
		st.PHDistinctID = id
		st.Company = r.company
		_ = saveState(r.home, st)
	}
}

func (r *recorder) groups() posthog.Groups {
	if r.company == "" {
		return nil
	}
	return posthog.NewGroups().Set(GroupCompany, r.company)
}

// enqueue starts the client on first use and drops the message on any error. r.mu is held.
func (r *recorder) enqueue(msg posthog.Message) {
	if r.client == nil {
		client, err := posthog.NewWithConfig(projectToken, posthog.Config{
			Endpoint:               endpoint,
			IsServer:               posthog.Ptr(false),
			ShutdownTimeout:        FlushTimeout,
			BatchUploadTimeout:     FlushTimeout,
			MaxRetries:             posthog.Ptr(0),
			Logger:                 quietLogger{},
			DefaultEventProperties: r.supers,
		})
		if err != nil {
			return
		}
		r.client = client
	}
	_ = r.client.Enqueue(msg)
}

// Close sends what is queued, waiting at most FlushTimeout, and turns telemetry off for the rest
// of the process. Call it before the process exits.
func Close() {
	mu.Lock()
	r := current
	current = nil
	mu.Unlock()
	r.close()
}

func (r *recorder) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	client := r.client
	r.client = nil
	r.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// quietLogger keeps posthog-go off the terminal: a failed send is not the user's problem.
type quietLogger struct{}

func (quietLogger) Debugf(string, ...interface{}) {}
func (quietLogger) Logf(string, ...interface{})   {}
func (quietLogger) Warnf(string, ...interface{})  {}
func (quietLogger) Errorf(string, ...interface{}) {}
