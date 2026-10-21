package check

import (
	"strings"
	"testing"
)

func TestUninstallStatusRequiresStructuralEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		want         Severity
	}{
		{"compact running", `{"service":{"running":true}}`, SevFail},
		{"pretty running", "{\n  \"service\": {\n    \"running\": true\n  }\n}", SevFail},
		{"stopped despite other running fields", `{"collector":{"running":true},"service":{"running":false},"script_forwarders":[{"running":true}]}`, ""},
		{"missing status", "", SevWarn},
		{"missing service", `{}`, SevWarn},
		{"missing running", `{"service":{}}`, SevWarn},
		{"null service", `{"service":null}`, SevWarn},
		{"null running", `{"service":{"running":null}}`, SevWarn},
		{"wrong type", `{"service":{"running":"false"}}`, SevWarn},
		{"malformed", `{"service":`, SevWarn},
		{"clipped legacy", `{"version":"1.3.91","service":{"running":...`, SevWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := clean()
			r.Status = tc.status
			findings := findingsFor(r)
			if tc.want != "" && !has(findings, "uninstall.status_agrees", tc.want) {
				t.Fatalf("status observation needs %s finding: %#v", tc.want, findings)
			}
			if tc.want == "" && (has(findings, "uninstall.status_agrees", SevFail) || has(findings, "uninstall.status_agrees", SevWarn)) {
				t.Fatalf("explicitly stopped service was not verified: %#v", findings)
			}
			if tc.want == SevWarn && has(findings, "uninstall.status_agrees", SevInfo) {
				t.Fatalf("unknown status was reported as verified: %#v", findings)
			}
		})
	}
}

func TestUninstallStatusReportsStayBounded(t *testing.T) {
	r := clean()
	r.Output = strings.Repeat("uninstall diagnostic\n", 1000)
	r.ExitCode = "1"
	r.Status = strings.Repeat("malformed status\n", 1000)
	var v Verdict
	Uninstall(&v, r)
	v.Resolve()
	if !has(v.Findings, "uninstall.status_agrees", SevWarn) {
		t.Fatal("malformed status must be unverified")
	}
	if len(v.Report()) > 2500 {
		t.Fatalf("human report includes unbounded output (%d bytes)", len(v.Report()))
	}
}
