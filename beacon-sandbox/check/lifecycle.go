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
