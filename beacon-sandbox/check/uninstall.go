package check

import (
	"fmt"
	"strings"
)

// Removal is what the runner observed after asking Beacon to uninstall itself.
//
// Every field is a string rather than a bool, and deliberately so: an empty value means the probe did
// not make that observation, which is a third state distinct from true and false. Collapsing it into
// false would let a probe that never ran report a removed service, and reporting a removal that was
// never checked is the exact failure this whole check exists to prevent.
type Removal struct {
	// Ran is "true" when the scenario asked for an uninstall at all.
	Ran string
	// ExitCode is `endpoint uninstall`'s status, as text.
	ExitCode string
	// Output is what it printed.
	Output string
	// ServiceKind and ServiceLabel identify what was registered at install time.
	ServiceKind  string
	ServiceLabel string
	// ServiceGone is "true"/"false" when the service manager was asked, empty when it was not.
	ServiceGone  string
	ServiceQuery string
	// LogRetained is "true"/"false" when the path was checked, empty otherwise.
	//
	// Only the log. `endpoint uninstall` removes everything in its install manifest -- which includes
	// config.json and otelcol.yaml -- and --keep-config does not change that, because it governs
	// *harness* telemetry settings rather than Beacon's own configuration. Retaining that is a
	// contract the packaging layer keeps by stashing the files around a removal, so asserting it at
	// this level would fail every correct run.
	LogRetained string
	// Status is `endpoint status --json` after the removal, which is Beacon's own account of itself.
	Status string
}

// Uninstall judges whether a removal actually removed anything.
//
// The failure this is aimed at is specific and has happened: an uninstall that reports success while
// the service stays registered with automatic start, so the collector returns at the next reboot after
// the operator was told it was gone. Nothing in the suite asked that question before -- every scenario
// installed, captured, and stopped -- so it was caught by a reviewer reading a diff, which is not a
// mechanism that scales.
//
// Two questions, and they are asked of two different authorities. Whether Beacon *thinks* it is
// uninstalled comes from its own status output. Whether the service is still registered is a fact
// about the machine, obtained from the service manager. Those are exactly the two that came apart in
// the bug, so a check that only asked Beacon would have passed.
func Uninstall(v *Verdict, r Removal) {
	if r.Ran != "true" {
		// The scenario did not ask. Silence is right: not every scenario should uninstall, and an
		// absent check must not be reported as a passing one.
		return
	}

	if r.ExitCode != "0" {
		v.Add(Finding{
			Check:    "uninstall.command_succeeded",
			Severity: SevFail,
			Summary:  fmt.Sprintf("`endpoint uninstall` exited %s: %s", r.ExitCode, oneLineOf(r.Output)),
			Why: "An uninstall that cannot complete leaves the endpoint in a state no one asked for: " +
				"binaries possibly gone, service possibly still registered.",
		})
	}

	switch r.ServiceGone {
	case "true":
		v.Add(Finding{
			Check:    "uninstall.service_removed",
			Severity: SevInfo,
			Summary:  fmt.Sprintf("the %s service %q is no longer registered", r.ServiceKind, r.ServiceLabel),
		})
	case "false":
		v.Add(Finding{
			Check:    "uninstall.service_removed",
			Severity: SevFail,
			Summary: fmt.Sprintf("the %s service %q is still registered after uninstall: %s",
				r.ServiceKind, r.ServiceLabel, oneLineOf(r.ServiceQuery)),
			Why: "A service left registered with automatic start brings the collector back at the next " +
				"reboot, after the operator was told it was removed. Uninstall reporting success while " +
				"this is true is the failure this check exists for.",
		})
	default:
		// Unverified, not clean. The supervised backend has no manager to ask, and a probe that could
		// not identify what was registered has no question to put to one.
		v.Add(Finding{
			Check:    "uninstall.service_removed",
			Severity: SevWarn,
			Summary: fmt.Sprintf("the service manager was not asked whether %q is gone (kind=%q)",
				r.ServiceLabel, r.ServiceKind),
			Why: "Removal of the service registration is unverified for this run. It is not evidence " +
				"that anything was left behind, and it is not evidence that nothing was.",
		})
	}

	// Retention is the other half of the contract, and it fails in the opposite direction: too much
	// removed rather than too little. An uninstall is often the first half of a reinstall, and
	// destroying collected telemetry is a separate, deliberate act.
	for name, retained := range map[string]string{
		"runtime log": r.LogRetained,
	} {
		switch retained {
		case "true":
			v.Add(Finding{
				Check:    "uninstall.data_retained",
				Severity: SevInfo,
				Summary:  fmt.Sprintf("the %s survived the uninstall, as --keep-logs asks", name),
			})
		case "false":
			v.Add(Finding{
				Check:    "uninstall.data_retained",
				Severity: SevFail,
				Summary:  fmt.Sprintf("the %s was removed despite --keep-logs", name),
				Why:      "Removing the product must not destroy what it collected unless a purge asks for it.",
			})
		default:
			v.Add(Finding{
				Check:    "uninstall.data_retained",
				Severity: SevWarn,
				Summary:  fmt.Sprintf("whether the %s survived was not checked", name),
				Why:      "Retention is unverified for this run rather than confirmed.",
			})
		}
	}

	// Beacon's own account, checked against the machine's. A status still describing a running
	// collector after a successful removal means the two disagree, and the disagreement is worth a
	// finding of its own even when the service really is gone.
	if strings.Contains(r.Status, `"running":true`) {
		v.Add(Finding{
			Check:    "uninstall.status_agrees",
			Severity: SevFail,
			Summary:  "`endpoint status` still reports a running collector after uninstall",
			Why: "Beacon's view of itself and the machine's have to agree, or one of them is lying to " +
				"whoever reads it next.",
		})
	}
}

// oneLineOf keeps a finding's detail to one line. Multi-line output in a verdict makes the report
// unreadable, which is how a real failure gets skimmed past.
func oneLineOf(s string) string {
	flat := strings.Join(strings.Fields(s), " ")
	if len(flat) > 300 {
		return flat[:300] + "…"
	}
	return flat
}
