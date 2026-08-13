package check

// Lifecycle is what the runner observed about reinstalling and about an unprivileged removal.
//
// Strings rather than bools, for the same reason as Removal: empty means the probe did not make the
// observation, which is a third state. Reading "did not run" as false would let a probe that never
// executed report a passing result, which is the shape of failure these checks exist to prevent.
type Lifecycle struct {
	// Restarted is "true"/"false" when a reinstall was performed, empty when it was not.
	Restarted    string
	PIDBefore    string
	PIDAfter     string
	ReinstallErr string

	// UnprivilegedRefused is "true"/"false" when an unprivileged removal was attempted.
	UnprivilegedRefused string
	UnprivilegedOutput  string

	// RollbackInstallRC, RollbackCollectorCount and RollbackStatus describe a reinstall that was
	// made to fail on purpose. Empty when the scenario did not ask.
	RollbackInstallRC      string
	RollbackCollectorCount string
	RollbackStatus         string
}

// Reinstall judges whether installing over a running endpoint replaced the collector.
//
// A reinstall rewrites the collector config, and during a package upgrade the package manager has
// already replaced the collector binary underneath the running process. If install only ever calls
// Load -- a no-op on a live service in every backend -- the previous collector keeps serving: it
// holds a deleted inode, ignores the new config, answers on the ports, and reports healthy. The
// endpoint stays a version behind until the machine reboots.
//
// The pid is the only evidence that separates the two cases. "Is it running?" is true in both.
func Reinstall(v *Verdict, l Lifecycle) {
	if l.Restarted == "" && l.ReinstallErr == "" {
		return // the scenario did not ask
	}
	if l.ReinstallErr != "" {
		v.Add(Finding{
			Check:    "reinstall.restarted",
			Severity: SevWarn,
			Summary:  l.ReinstallErr,
			Why: "Recorded as unproven rather than passed. The probe could not establish whether a " +
				"reinstall replaces the collector, which is not evidence that it does.",
		})
		return
	}
	if l.Restarted == "true" {
		v.Add(Finding{
			Check:    "reinstall.restarted",
			Severity: SevInfo,
			Summary:  "installing over a running endpoint replaced the collector (pid " + l.PIDBefore + " -> " + l.PIDAfter + ")",
		})
		return
	}
	v.Add(Finding{
		Check:    "reinstall.restarted",
		Severity: SevFail,
		Summary: "the collector kept pid " + l.PIDBefore + " across a reinstall, so it is still " +
			"running the binary and configuration from before it",
		Why: "An upgrade that leaves the previous collector serving looks entirely healthy -- ports " +
			"answer, status reports running -- while exporting through a version the package manager " +
			"has already deleted. Install must restart a service it finds already running.",
	})
}

// FailedReinstallRollback judges what a deliberately failed reinstall left behind.
//
// The contract is that a failed install returns the machine to the endpoint it had: one collector,
// running, on the configuration from before the attempt. Two ways of breaking it have shipped.
//
// Too few collectors means rollback took a healthy endpoint down -- and on systemd it also disables
// the unit, so the machine stays down across a reboot. Too many means rollback stopped the wrong
// process: the collector the failed install started keeps running on the new config, holding the
// ports, while another is started beside it. Asking whether a collector is running cannot tell any
// of these apart, because the answer is yes in every one.
func FailedReinstallRollback(v *Verdict, l Lifecycle) {
	if l.RollbackInstallRC == "" {
		return // the scenario did not ask
	}

	// A reinstall pointed at a program that never listens has to fail. If it reported success,
	// readiness is not being checked and the rest of this proves nothing.
	if l.RollbackInstallRC == "0" {
		v.Add(Finding{
			Check:    "rollback.install_failed",
			Severity: SevFail,
			Summary:  "a reinstall pointed at a collector that never listens reported success",
			Why: "Install is supposed to wait for the collector to become ready and roll back when " +
				"it does not. Reporting success here means an endpoint that cannot collect is " +
				"indistinguishable from one that can.",
		})
		return
	}

	switch l.RollbackCollectorCount {
	case "1":
		v.Add(Finding{
			Check:    "rollback.left_one_collector",
			Severity: SevInfo,
			Summary:  "a failed reinstall left exactly one collector running",
		})
	case "", "0":
		v.Add(Finding{
			Check:    "rollback.left_one_collector",
			Severity: SevFail,
			Summary:  "a failed reinstall left no collector running, so it took a working endpoint down with it",
			Why: "Rollback may undo what the failed install did; it may not remove what was working " +
				"beforehand. On systemd it also disables the unit, so the machine stays down across " +
				"a reboot -- an upgrade that fails and ends collection is worse than one that fails.",
		})
	default:
		v.Add(Finding{
			Check:    "rollback.left_one_collector",
			Severity: SevFail,
			Summary:  "a failed reinstall left " + l.RollbackCollectorCount + " collectors running",
			Why: "Rollback stopped the wrong process. The collector the failed install started is " +
				"still running on the configuration that failed, holding the OTLP ports, while " +
				"another was started beside it -- and the pidfile tracks only one of them.",
		})
	}
}

// UnprivilegedUninstall judges whether a system removal without privileges was refused.
//
// verify_uninstall cannot catch this, because it removes the endpoint elevated, which is the path
// that works. The failure worth catching is the other one: an unprivileged `uninstall --system`
// that printed "Endpoint service, config, and managed files removed." and exited 0 while every
// operation it attempted had failed, leaving the unit enabled and the collector due back at the
// next reboot.
func UnprivilegedUninstall(v *Verdict, l Lifecycle) {
	switch l.UnprivilegedRefused {
	case "":
		return // the scenario did not ask
	case "true":
		v.Add(Finding{
			Check:    "uninstall.unprivileged_refused",
			Severity: SevInfo,
			Summary:  "an unprivileged system uninstall was refused",
		})
	default:
		v.Add(Finding{
			Check:    "uninstall.unprivileged_refused",
			Severity: SevFail,
			Summary:  "an unprivileged `endpoint uninstall --system` exited 0: " + oneLineOf(l.UnprivilegedOutput),
			Why: "Reporting success for a removal it had no privileges to perform tells the operator " +
				"the endpoint is gone while the service is still registered. It returns at the next " +
				"reboot, after they were told otherwise.",
		})
	}
}
