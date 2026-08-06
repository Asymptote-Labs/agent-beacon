package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/dispatch"
)

// githubChecks are the prerequisites for a dispatched Windows run.
//
// Different in kind from the Modal ones, and the difference is worth stating: the workflow builds
// both `beacon.exe` and `beacon-otelcol.exe` from the ref it runs, so the stale-binary trap that
// `collector_freshness` guards cannot happen there. It is replaced by a sharper one -- a dispatch
// tests the *pushed ref*, so uncommitted or unpushed work is silently not under test.
func githubChecks(root, repo string) []doctorCheck {
	var out []doctorCheck

	if _, err := exec.LookPath("gh"); err != nil {
		return append(out, doctorCheck{Name: "gh_cli", Status: statusFail,
			Detail: "gh not found on PATH",
			Fix:    "install the GitHub CLI from https://cli.github.com/"})
	}
	if b, err := exec.Command("gh", "auth", "status").CombinedOutput(); err != nil {
		out = append(out, doctorCheck{Name: "gh_auth", Status: statusFail,
			Detail: firstLine(string(b)),
			Fix:    "gh auth login"})
	} else {
		out = append(out, doctorCheck{Name: "gh_auth", Status: statusOK, Detail: "authenticated"})
	}

	// The workflow has to exist on the *default branch* for workflow_dispatch to be offered at
	// all, which is the one GitHub Actions rule most likely to be met with "but the file is right
	// there". Checked locally first, then remotely, so the two failures are distinguishable.
	wf := filepath.Join(root, ".github", "workflows", dispatch.Workflow)
	if _, err := os.Stat(wf); err != nil {
		out = append(out, doctorCheck{Name: "windows_workflow", Status: statusFail,
			Detail: "missing " + wf,
			Fix:    "this checkout does not contain the Windows sandbox workflow; update it"})
	} else {
		out = append(out, workflowDispatchableCheck(repo))
	}

	out = append(out, dispatchRefCheck(root))
	out = append(out, windowsSecretCheck(repo))
	return out
}

// workflowDispatchableCheck asks GitHub whether the workflow can actually be dispatched.
//
// Queried against the repository rather than the checkout, so it catches the case where the file
// exists locally but the branch carrying it has not reached the default branch -- GitHub only
// offers workflow_dispatch for workflows present there, and `gh workflow run` then fails with a
// bare "could not find any workflows".
func workflowDispatchableCheck(repo string) doctorCheck {
	args := []string{"workflow", "list", "--all", "--json", "name,state,path"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	b, err := exec.Command("gh", args...).CombinedOutput()
	if err != nil {
		return doctorCheck{Name: "windows_workflow", Status: statusWarn,
			Detail: "could not list the repository's workflows (" + firstLine(string(b)) + ")",
			Fix:    "check `gh workflow list` by hand; a dispatch will fail if the workflow is absent"}
	}
	var workflows []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(b, &workflows); err != nil {
		return doctorCheck{Name: "windows_workflow", Status: statusWarn,
			Detail: "could not parse `gh workflow list` output: " + firstLine(err.Error()),
			Fix:    "check `gh workflow list` by hand"}
	}
	for _, w := range workflows {
		if filepath.Base(w.Path) != dispatch.Workflow {
			continue
		}
		if !strings.EqualFold(w.State, "active") {
			return doctorCheck{Name: "windows_workflow", Status: statusFail,
				Detail: "the workflow is " + w.State,
				Fix:    "gh workflow enable " + dispatch.Workflow}
		}
		return doctorCheck{Name: "windows_workflow", Status: statusOK,
			Detail: dispatch.Workflow + " is active and dispatchable"}
	}
	return doctorCheck{Name: "windows_workflow", Status: statusFail,
		Detail: dispatch.Workflow + " exists in this checkout but the repository does not list it",
		Fix: "GitHub only offers workflow_dispatch for workflows on the default branch; " +
			"merge or push it there first"}
}

// dispatchRefCheck is the github provider's equivalent of the collector-freshness trap.
//
// A dispatch runs a *ref*, not the working tree, so local edits that have not been pushed are not
// under test -- and the run will pass or fail on code the contributor is not looking at. That is
// the same class of wasted investigation the stale-collector warning exists to prevent, so it gets
// the same treatment: a warning naming exactly what is not covered, satisfiable by doing what it
// asks.
func dispatchRefCheck(root string) doctorCheck {
	// Only the trees a Windows run actually exercises. Warning on unrelated edits would make the
	// check fire constantly and be ignored.
	watched := []string{"cli", "collector-builder", "beacon-sandbox", ".github/workflows"}
	args := append([]string{"-C", root, "status", "--porcelain", "--"}, watched...)
	b, err := exec.Command("git", args...).Output()
	if err != nil {
		return doctorCheck{Name: "dispatch_ref", Status: statusWarn,
			Detail: "could not read git status, so it is unknown whether a dispatch would test your work",
			Fix:    "check `git status` by hand before relying on the result"}
	}
	var dirty []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			dirty = append(dirty, line)
		}
	}
	if len(dirty) > 0 {
		return doctorCheck{Name: "dispatch_ref", Status: statusWarn,
			Detail: fmt.Sprintf("%d uncommitted change(s) under %s; a dispatched run tests the "+
				"pushed ref, so these are NOT under test", len(dirty), strings.Join(watched, ", ")),
			Fix: "commit and push, then dispatch with --ref <branch>"}
	}

	// Committed but unpushed is the subtler half: `git status` is clean, so everything looks fine,
	// and the dispatch quietly runs the previous commit.
	if b, err := exec.Command("git", "-C", root, "rev-list", "--count", "@{upstream}..HEAD").Output(); err == nil {
		if n := strings.TrimSpace(string(b)); n != "" && n != "0" {
			return doctorCheck{Name: "dispatch_ref", Status: statusWarn,
				Detail: n + " commit(s) ahead of the upstream branch; a dispatch would run the " +
					"pushed ref, which does not include them",
				Fix: "git push, then dispatch with --ref <branch>"}
		}
	}
	return doctorCheck{Name: "dispatch_ref", Status: statusOK,
		Detail: "the working tree matches the pushed ref, so a dispatch tests what you are looking at"}
}

// windowsSecretCheck reports whether the agent credential the workflow needs is configured.
//
// Reported as a warning rather than a failure when it cannot be read: listing environment secrets
// needs repository admin, so a contributor without it would otherwise be told the setup is broken
// when it is merely unverifiable from here. The distinction is the usual one -- "I could not check"
// must never render as either a pass or a failure.
func windowsSecretCheck(repo string) doctorCheck {
	const env = "windows-sandbox"
	args := []string{"secret", "list", "--env", env}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	setCmd := "printf '%s' \"$ANTHROPIC_API_KEY\" | gh secret set ANTHROPIC_API_KEY --env " + env
	b, err := exec.Command("gh", args...).CombinedOutput()
	if err != nil {
		// A 404 is a different problem from a permission error, and they need opposite actions:
		// one means create the environment, the other means ask somebody who can see it. Reporting
		// both as "could not check" would send a reader to the wrong place.
		if strings.Contains(string(b), "404") {
			return doctorCheck{Name: "windows_secret", Status: statusFail,
				Detail: "the " + env + " environment does not exist, so the workflow cannot resolve its secret",
				Fix: "gh api -X PUT repos/{owner}/{repo}/environments/" + env +
					"    then: " + setCmd}
		}
		return doctorCheck{Name: "windows_secret", Status: statusWarn,
			Detail: "could not list the " + env + " environment's secrets (" + firstLine(string(b)) + ")",
			Fix:    "listing needs repo admin; if a run fails to authenticate, set it with: " + setCmd}
	}
	if !strings.Contains(string(b), "ANTHROPIC_API_KEY") {
		return doctorCheck{Name: "windows_secret", Status: statusFail,
			Detail: "the " + env + " environment has no ANTHROPIC_API_KEY, so the session cannot authenticate",
			Fix:    setCmd + "    (use a dedicated, budget-capped key)"}
	}
	return doctorCheck{Name: "windows_secret", Status: statusOK,
		Detail: "ANTHROPIC_API_KEY is set in the " + env + " environment"}
}

// localChecks are the prerequisites for running scenarios on this machine.
//
// Deliberately thin. The interesting question -- is this machine allowed to be mutated -- is
// answered by the provider itself at construction time, and duplicating that judgment here would
// give two places to disagree about it.
func localChecks() []doctorCheck {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return []doctorCheck{{Name: "disposable_host", Status: statusFail,
			Detail: "not running inside GitHub Actions, so this machine cannot be shown to be disposable",
			Fix: "use --provider github to dispatch to a runner, or pass --allow-host-mutation " +
				"if this machine is genuinely throwaway"}}
	}
	env := os.Getenv("RUNNER_ENVIRONMENT")
	if env != "" && env != "github-hosted" {
		return []doctorCheck{{Name: "disposable_host", Status: statusWarn,
			Detail: "this is a " + env + " runner, which persists between jobs, so an install here outlives the run",
			Fix:    "pass --allow-host-mutation only if this runner is genuinely disposable"}}
	}
	return []doctorCheck{{Name: "disposable_host", Status: statusOK,
		Detail: "github-hosted ephemeral runner"}}
}
