package check

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fwdCanary     = "BEACON_SANDBOX_fwd123"
	fwdVector     = "/opt/beacon/bin/vector"
	fwdValidation = `{"vendor":"beacon","event":{"action":"agent.detected"},"destination":{"type":"asymptote","mode":"asymptote_managed_http"},"message":"Beacon endpoint Asymptote validation event"}`
	fwdPrompt     = `{"vendor":"beacon","event":{"action":"prompt.submitted"},"prompt":{"text":"echo BEACON_SANDBOX_fwd123"}}`
	fwdOther      = `{"vendor":"beacon","event":{"action":"token.usage"}}`
)

func logOf(t *testing.T, name string, lines ...string) Log {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := ReadLog(p)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// healthyForward is a run where everything arrived: the validation event, a session event, every
// request authenticated, and every forwarded line present in the runtime log.
func healthyForward(t *testing.T) Forward {
	return Forward{
		Asked:         true,
		Ran:           "true",
		VectorExe:     fwdVector,
		WantVectorExe: fwdVector,
		Canary:        fwdCanary,
		Received:      logOf(t, "forwarded.ndjson", fwdValidation, fwdPrompt),
		Requests: []ForwardRequest{
			{Method: "GET", Path: "/v1/ingest/health", AuthOK: true},
			{Method: "POST", Path: "/v1/ingest/runtime", AuthOK: true, Lines: 2, Encoding: "gzip"},
		},
		Runtime: logOf(t, "runtime.jsonl", fwdOther, fwdValidation, fwdPrompt),
		Text:    map[string]string{"vector.out": "INFO vector: Vector has started."},
	}
}

func judgeForward(f Forward) Verdict {
	v := Verdict{Scenario: "i04"}
	Forwarding(&v, f)
	v.Resolve()
	return v
}

func failedChecks(v Verdict) string {
	var names []string
	for _, f := range v.Failures() {
		names = append(names, f.Check+": "+f.Summary)
	}
	return strings.Join(names, "\n")
}

func TestForwardingPassesAHealthyRun(t *testing.T) {
	v := judgeForward(healthyForward(t))
	if v.Outcome != Pass {
		t.Fatalf("a healthy forwarding run failed:\n%s", failedChecks(v))
	}
	if len(v.Findings) != 1 || v.Findings[0].Severity != SevInfo {
		t.Fatalf("want one info finding summarising delivery, got %+v", v.Findings)
	}
}

// A scenario that did not ask gets no finding at all, while one that asked and never ran fails.
func TestForwardingDistinguishesNotAskedFromNotRun(t *testing.T) {
	v := judgeForward(Forward{})
	if len(v.Findings) != 0 {
		t.Fatalf("a scenario that did not ask must produce nothing, got %+v", v.Findings)
	}
	v = judgeForward(Forward{Asked: true})
	if v.Outcome != Fail || !strings.Contains(failedChecks(v), "forwarding.probe_ran") {
		t.Fatalf("an asked-for probe that never ran must fail, got %s:\n%s", v.Outcome, failedChecks(v))
	}
}

// Each requirement has to be able to fail on its own. A mutation that fails every check at once
// would not show which one is doing the work.
func TestForwardingFailsEachRequirementIndependently(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Forward)
		check  string
	}{
		{"probe error", func(f *Forward) { f.Err = "vector exited before it was ready" }, "forwarding.probe_ran"},
		{"vector not observed", func(f *Forward) { f.VectorExe = "" }, "forwarding.vector_path"},
		{"another vector", func(f *Forward) { f.VectorExe = "/usr/bin/vector" }, "forwarding.vector_path"},
		{"unauthenticated batch", func(f *Forward) { f.Requests[1].AuthOK = false }, "forwarding.authenticated"},
		{"request log missing", func(f *Forward) { f.Requests, f.RequestsErr = nil, os.ErrNotExist }, "forwarding.authenticated"},
		{"no validation event", func(f *Forward) {
			f.Received = logOf(t, "forwarded.ndjson", fwdPrompt)
		}, "forwarding.delivered"},
		{"no session event", func(f *Forward) {
			f.Received = logOf(t, "forwarded.ndjson", fwdValidation)
		}, "forwarding.delivered"},
		{"nothing received", func(f *Forward) {
			f.Received, f.ReceivedErr = Log{}, os.ErrNotExist
		}, "forwarding.delivered"},
		{"altered line", func(f *Forward) {
			f.Received = logOf(t, "forwarded.ndjson", fwdValidation,
				strings.Replace(fwdPrompt, "prompt.submitted", "prompt.edited", 1))
		}, "forwarding.intact"},
		{"merged lines", func(f *Forward) {
			f.Received = logOf(t, "forwarded.ndjson", fwdValidation, fwdPrompt[:40])
		}, "forwarding.intact"},
		{"runtime log unreadable", func(f *Forward) { f.RuntimeErr = errors.New("gone") }, "forwarding.intact"},
		{"key in a forwarded event", func(f *Forward) {
			leaked := strings.Replace(fwdPrompt, `"}}`, ` bcn_device_test_0123456789abcdef"}}`, 1)
			f.Received = logOf(t, "forwarded.ndjson", fwdValidation, leaked)
			f.Runtime = logOf(t, "runtime.jsonl", fwdValidation, leaked)
		}, "forwarding.no_device_key_leak"},
		{"key in vector's output", func(f *Forward) {
			f.Text["vector.out"] = "token=bcn_device_test_0123456789abcdef"
		}, "forwarding.no_device_key_leak"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := healthyForward(t)
			tc.mutate(&f)
			v := judgeForward(f)
			if v.Outcome != Fail {
				t.Fatalf("want FAIL, got %s", v.Outcome)
			}
			if got := failedChecks(v); !strings.Contains(got, tc.check) {
				t.Fatalf("want a %s failure, got:\n%s", tc.check, got)
			}
		})
	}
}

// The session's events are the evidence that Vector kept tailing; the validation event alone must
// not count as one, even though the canary is not in it.
func TestForwardingDoesNotCountTheValidationEventAsSession(t *testing.T) {
	f := healthyForward(t)
	f.Canary = "asymptote" // present in the validation event, absent from the prompt
	v := judgeForward(f)
	if !strings.Contains(failedChecks(v), "no event from the agent session") {
		t.Fatalf("the validation event was counted as a session event:\n%s", failedChecks(v))
	}
}

func TestReadForwardRequestsRejectsAMalformedLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "requests.jsonl")
	body := `{"method":"GET","path":"/v1/ingest/health","auth_ok":true}` + "\nnot json\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadForwardRequests(p); err == nil {
		t.Fatal("a malformed request log must be an error, since our own stand-in wrote it")
	}
}

func TestDeviceKeyPatternMatchesTestAndRealKeys(t *testing.T) {
	for _, s := range []string{"bcn_device_test_0123456789abcdef0123456789abcdef", "Bearer bcn_device_Ab12Cd34Ef56"} {
		if !DeviceKeyPattern.MatchString(s) {
			t.Errorf("%q should match", s)
		}
	}
	for _, s := range []string{"device_key", "bcn_device_", "the bcn_device prefix"} {
		if DeviceKeyPattern.MatchString(s) {
			t.Errorf("%q should not match", s)
		}
	}
}
