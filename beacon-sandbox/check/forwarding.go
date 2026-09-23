package check

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
)

// DeviceKeyPattern matches an Asymptote device key, test or real.
//
// The leak checks search for the key's shape rather than its value, so they work offline too: the
// forwarding probe generates the key inside the guest and nothing on the host ever holds it. No
// Beacon event has any reason to contain this prefix, so any match is a disclosure.
var DeviceKeyPattern = regexp.MustCompile(`bcn_device_[A-Za-z0-9_]{8,}`)

// Forward is what the runner observed while the bundled Vector forwarded Beacon's runtime log to a
// loopback stand-in for the ingest service.
//
// Ran, Err and VectorExe are strings for the same reason as Removal's fields: empty means the probe
// did not make that observation, which must never read as a pass.
type Forward struct {
	// Asked is whether the scenario requested the probe. Without it, a run whose probe never started
	// would look the same as a scenario that never asked.
	Asked bool
	Ran   string
	// Err is the first thing that went wrong while setting up or collecting.
	Err string
	// VectorExe is the executable of the running Vector process, from /proc/<pid>/exe.
	VectorExe     string
	WantVectorExe string
	// Canary is the run's canary, which the session's own events carry.
	Canary string

	// Received is the runtime NDJSON the stand-in accepted, and ReceivedErr why it could not be read.
	Received    Log
	ReceivedErr error
	// Requests is the stand-in's request log.
	Requests    []ForwardRequest
	RequestsErr error
	// Runtime is Beacon's runtime log as it stood after forwarding finished, to check the forwarded
	// lines are Beacon's own bytes.
	Runtime    Log
	RuntimeErr error
	// Text is other collected output, such as Vector's own log, keyed by artifact name, searched
	// for a device key.
	Text map[string]string
}

// ForwardRequest is one request the stand-in served. It records whether the bearer key matched the
// secrets file, never the header itself.
type ForwardRequest struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	AuthOK   bool   `json:"auth_ok"`
	Lines    int    `json:"lines"`
	Encoding string `json:"encoding"`
}

// ReadForwardRequests parses the stand-in's request log. A malformed line is an error: the log is
// written by our own stand-in, so a bad line means the probe is broken.
func ReadForwardRequests(path string) ([]ForwardRequest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ForwardRequest
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r ForwardRequest
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return out, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Asymptote's validation event, written by `beacon endpoint asymptote validate`.
const (
	asymptoteDestinationType = "asymptote"
	asymptoteDestinationMode = "asymptote_managed_http"
)

func isAsymptoteValidation(e Event) bool {
	if e.ParseErr != nil {
		return false
	}
	typ, _ := e.Field("destination.type")
	mode, _ := e.Field("destination.mode")
	return typ == asymptoteDestinationType && mode == asymptoteDestinationMode
}

// Forwarding judges whether the bundled Vector delivered Beacon's events, intact and authenticated.
//
// Four things have to hold, and each is a separate way this has broken or could:
//
//   - The Vector that ran is the one the packages install. The probe starts it by that path, so a
//     mismatch means the image or the probe is wrong, and the run would be testing some other
//     binary.
//   - Every request carried the device key from the secrets file. That is the secret backend
//     working; a wrong or missing key is exactly what a revoked-looking endpoint would send.
//   - The validation event and at least one event from the session arrived. The validation event
//     is written right after Vector starts; the session's events are written minutes later, so they
//     prove Vector kept tailing the file rather than shipping one batch and stopping.
//   - Every forwarded line is a line from Beacon's runtime log, byte for byte. Standard mode ships
//     retained content unmodified, so any difference is Vector or the pack altering events.
//
// No device key may appear in anything collected.
func Forwarding(v *Verdict, f Forward) {
	if !f.Asked {
		return
	}
	fail := func(check, summary, why string, evidence ...string) {
		v.Add(Finding{Check: check, Severity: SevFail, Summary: summary, Why: why, Evidence: cap5(evidence)})
	}
	if f.Ran != "true" {
		fail("forwarding.probe_ran", "the forwarding probe did not run, so nothing is known about "+
			"whether the bundled Vector forwards",
			"An unrun probe is not a pass. The scenario exists to run Vector; a run that did not is a "+
				"broken run.")
		return
	}
	if f.Err != "" {
		fail("forwarding.probe_ran", f.Err,
			"The probe could not finish, so delivery is unproven. Check vector.out in the run directory.")
	}

	switch {
	case f.VectorExe == "":
		fail("forwarding.vector_path", "the running Vector's executable was not observed",
			"Without it the run cannot say which Vector forwarded.")
	case f.VectorExe != f.WantVectorExe:
		fail("forwarding.vector_path",
			fmt.Sprintf("Vector ran from %s, not %s", f.VectorExe, f.WantVectorExe),
			"The probe is meant to exercise the Vector the Linux packages install. A different "+
				"binary means the result says nothing about the bundled one.")
	}

	runtimePosts := 0
	var badAuth []string
	switch {
	case f.RequestsErr != nil:
		fail("forwarding.authenticated", "the stand-in's request log could not be read: "+f.RequestsErr.Error(),
			"Without it there is no record that Vector sent the device key.")
	default:
		for i, r := range f.Requests {
			if r.Method == "POST" && r.Path == "/v1/ingest/runtime" {
				runtimePosts++
			}
			if !r.AuthOK {
				badAuth = append(badAuth, fmt.Sprintf("request %d: %s %s", i+1, r.Method, r.Path))
			}
		}
		if len(badAuth) > 0 {
			fail("forwarding.authenticated",
				fmt.Sprintf("%d request(s) did not carry the device key from the secrets file", len(badAuth)),
				"Vector reads the key through its secret backend. A request without it is what a "+
					"revoked device looks like to the ingest service, so every batch would be refused.",
				badAuth...)
		}
	}

	if f.ReceivedErr != nil && !errors.Is(f.ReceivedErr, fs.ErrNotExist) {
		fail("forwarding.delivered", "the forwarded events could not be read: "+f.ReceivedErr.Error(), "")
	}
	var validation, session int
	var unparseable, altered []string
	runtimeLines := map[string]bool{}
	for _, e := range f.Runtime.Events {
		runtimeLines[e.Raw] = true
	}
	for _, e := range f.Received.Events {
		loc := fmt.Sprintf("%s:%d", f.Received.Path, e.Line)
		if e.ParseErr != nil {
			unparseable = append(unparseable, loc)
			continue
		}
		if isAsymptoteValidation(e) {
			validation++
		} else if f.Canary != "" && strings.Contains(e.Raw, f.Canary) {
			session++
		}
		if f.RuntimeErr == nil && !runtimeLines[e.Raw] {
			altered = append(altered, loc)
		}
	}
	if len(unparseable) > 0 {
		fail("forwarding.intact", fmt.Sprintf("%d forwarded line(s) are not valid JSON", len(unparseable)),
			"Beacon writes one JSON object per line and Vector frames them by newline. A partial or "+
				"merged line means the framing is wrong, and ingest would reject it.",
			unparseable...)
	}
	switch {
	case f.RuntimeErr != nil:
		fail("forwarding.intact", "the runtime log could not be re-read after forwarding: "+f.RuntimeErr.Error(),
			"Without it the forwarded lines cannot be compared with what Beacon wrote.")
	case len(altered) > 0:
		fail("forwarding.intact",
			fmt.Sprintf("%d forwarded line(s) do not appear verbatim in Beacon's runtime log", len(altered)),
			"Standard mode ships Beacon's lines unchanged. A line that differs was altered or "+
				"invented between the file and the ingest service.",
			altered...)
	}
	if validation == 0 {
		fail("forwarding.delivered", "the validation event never reached the stand-in",
			"`beacon endpoint asymptote validate` writes it right after Vector starts. If it did not "+
				"arrive, Vector is not shipping the runtime log at all.")
	}
	if session == 0 {
		fail("forwarding.delivered", "no event from the agent session reached the stand-in",
			"The session's events carry this run's canary and are written minutes after Vector "+
				"starts. Without them the run only shows that Vector shipped its first batch.")
	}

	var leaks []string
	for _, l := range []Log{f.Received, f.Runtime} {
		for _, e := range l.Events {
			if DeviceKeyPattern.MatchString(e.Raw) {
				leaks = append(leaks, fmt.Sprintf("%s:%d", l.Path, e.Line))
			}
		}
	}
	names := make([]string, 0, len(f.Text))
	for name := range f.Text {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if DeviceKeyPattern.MatchString(f.Text[name]) {
			leaks = append(leaks, name)
		}
	}
	if len(leaks) > 0 {
		fail("forwarding.no_device_key_leak", "a device key appears in collected output",
			"The key's one home is the 0600 secrets file. Anywhere else it is a disclosure.",
			leaks...)
	}

	if len(v.Failures()) == 0 {
		v.Add(Finding{
			Check:    "forwarding.delivered",
			Severity: SevInfo,
			Summary: fmt.Sprintf("the bundled Vector (%s) forwarded %d event(s) in %d authenticated "+
				"request(s): the validation event and %d from the session", f.VectorExe,
				len(f.Received.Events), runtimePosts, session),
		})
	}
}
