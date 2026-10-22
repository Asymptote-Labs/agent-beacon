package check

import "testing"

func TestUninstallStatusRequiresStructuralEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		want         Severity
	}{
		{"pretty running", "{\n  \"service\": {\n    \"running\": true\n  }\n}", SevFail},
		{"stopped despite other running fields", `{"collector":{"running":true},"service":{"running":false},"script_forwarders":[{"running":true}]}`, ""},
		{"missing status", "", SevWarn},
		{"null running", `{"service":{"running":null}}`, SevWarn},
		{"wrong type", `{"service":{"running":"false"}}`, SevWarn},
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
		})
	}
}
