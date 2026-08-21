# Beacon Installation and Onboarding Feedback

## Executive summary

Beacon needs a shorter, more guided path from “I heard about Beacon” to “I can see activity from
my local agents.” The desired experience is:

1. Copy and paste one installation command.
2. Receive a clear, actionable fix for anything that blocks or limits the installation.
3. Run a guided getting-started flow that inspects the current machine.
4. Configure relevant runtimes and hooks with minimal prior knowledge.
5. Open the dashboard and confirm that real events are arriving.

The current pieces largely exist—installation, discovery, doctor, hook management, status, and the
dashboard—but the user has to discover and assemble the workflow. The Beacon CLI should connect
these pieces into one coherent onboarding experience and should never leave the user wondering
what to do next.

## Revised implementation direction: keep onboarding in Go

An initial approach placed most of the workflow in a downloaded shell installer. Testing exposed
an important architectural problem: the shell duplicated behavior already owned by Beacon, could
misinterpret child-process output, and behaved differently depending on which released Beacon
binary it downloaded. It also made the onboarding logic harder to reuse from repair, packages, and
direct CLI invocation.

The preferred design is now:

```text
download release archive → verify archive → extract binaries → run Beacon
                                                        ↓
                              Go CLI owns the complete onboarding experience
```

The documented shell commands should remain deliberately small. They should:

1. Resolve the latest stable release.
2. Map the host architecture to a published archive.
3. Download the archive and `checksums.txt`.
4. Verify the exact archive before extraction.
5. Place the three binaries on `PATH`.
6. Invoke `beacon endpoint install` directly in the user's terminal.

Everything involving operating-system state, error interpretation, remediation, runtime
discovery, hook installation, health validation, and next steps should live in the Go project.
This provides one behavior whether Beacon came from a tarball, Homebrew, a package, a local build,
or a future distribution channel.

### Why this matters for the linger failure

The published Beacon v1.2.1 binary successfully installed and ran the collector even though its
attempt to enable systemd linger emitted the missing-`pkttyagent` message. Direct verification
showed that:

- The release binary executed normally.
- The systemd user service was enabled and active.
- Both OTLP ports were listening.
- The runtime log contained hundreds of events.
- Live Codex events continued to arrive.

The failure concerned logout persistence, not Beacon's ability to run during the current login
session. That distinction belongs in Beacon's lifecycle result and diagnostics. A wrapper script
should not have to infer it from terminal output—especially because `pkttyagent` can write directly
to the controlling terminal instead of the stderr stream a wrapper captures.

The Go implementation should therefore report a truthful degraded-success state:

```text
✓ Collector is installed and running
! Collection will stop after logout because linger is disabled
  Fix: sudo loginctl enable-linger <user>
```

After the fix is applied, Beacon can verify the resulting state with:

```bash
loginctl show-user "$USER" --property=Linger --value
```

This is more accurate and useful than either silently declaring complete success or treating the
entire endpoint installation as failed.

## Feedback from the Linux installation experience

### The primary goal is a dependable copy-and-paste installation

A new user should be able to copy one documented command sequence, paste it into a terminal, and
get a working Beacon installation. The shell portion should remain narrow: resolve the stable
release, select the architecture, download and verify the archive, extract the binaries, and invoke
Beacon. Host inspection, prerequisite handling, remediation, runtime configuration, health checks,
and guided onboarding belong in the Go CLI, where they can be tested and reused across install,
repair, doctor, and future packaging paths.

The download instructions should:

- Detect the operating system and architecture.
- Resolve the latest stable Beacon release.
- Download the correct release archive.
- Verify the exact archive before extracting it.
- Install all required binaries in an appropriate user or system location.
- Invoke `beacon endpoint install` directly in the user's terminal.

The Go CLI should then check required operating-system capabilities, configure and start the
endpoint, verify collector health, report precisely what succeeded, and guide the user through any
remaining work.

### Missing system configuration must produce a solution

The Linux installation encountered a systemd user-service persistence issue. The account did not
have systemd linger enabled, so the collector would not survive logout. Beacon attempted to enable
linger through `loginctl`, which tried to launch `/usr/bin/pkttyagent`. That authentication agent
was not installed, producing this message:

```text
Failed to execute /usr/bin/pkttyagent: No such file or directory
```

The endpoint command nevertheless continued and printed successful installation output. From the
user's perspective, an explicit failure had appeared in the middle of an otherwise successful
transcript, with no immediate explanation or remedy.

The direct solution on this machine was:

```bash
sudo loginctl enable-linger "$USER"
```

The result can be verified with:

```bash
loginctl show-user "$USER" --property=Linger --value
```

Expected output:

```text
yes
```

Beacon should avoid interactive PolicyKit-agent discovery for this optional configuration. It
should check the linger state, attempt a noninteractive operation when appropriate, verify the
result, and then do one of the following:

- Continue quietly when linger is already enabled or was enabled successfully.
- Present and optionally run the exact `sudo loginctl enable-linger <user>` remediation.
- Clearly explain the limited operating mode if the user chooses not to enable linger.

The broader principle is that detecting a missing prerequisite is not enough. Every detected gap
should include a concrete solution. If Beacon can fix it safely, it should offer to do so. If
administrator approval is required, Beacon should print the exact command and explain why it is
needed.

### Success output must reflect the actual result

The installer should not print an unconditional message such as:

```text
beacon-install: installation complete
```

after an unresolved error. It should distinguish among:

- Fully installed and ready.
- Installed and running with a clearly described limitation.
- Partially installed and requiring a specific remediation.
- Failed, with the failed stage and recovery steps identified.

Warnings written directly to the terminal by a child authentication process may not be captured
through ordinary stderr redirection. Validation should therefore check resulting system state—not
only command exit codes or captured output. For linger, the authoritative check is whether
`loginctl show-user ... --property=Linger --value` returns `yes`.

## Proposed guided onboarding experience

### Add `beacon getting-started`

After installation, Beacon should offer to launch a guided command such as:

```bash
beacon getting-started
```

At the end of installation, the prompt could be:

```text
Beacon is installed. Run guided setup now? [Y/n]
```

The default should be yes, so pressing Enter starts onboarding. Noninteractive installations
should skip the prompt and print the command as the next step.

This command should build on `beacon endpoint doctor` but serve a different purpose:

- `doctor` diagnoses the health of an existing configuration.
- `getting-started` discovers the machine, recommends a setup, helps apply it, and proves that it
  works.

The command may also expose a short alias:

```bash
beacon start
```

Useful automation controls include:

```bash
beacon getting-started --yes
beacon getting-started --no-hooks
beacon getting-started --no-dashboard
```

Normal package, CI, and other noninteractive installs must never block on these prompts. They
should print `beacon getting-started` as the next action instead.

### Inspect the current machine

The guided flow should begin by detecting:

- Supported runtimes installed on the machine.
- Which runtimes already have Beacon telemetry or hooks configured.
- Which runtimes need hooks, OpenTelemetry configuration, a restart, or another manual action.
- Collector and service health.
- User-service persistence requirements such as systemd linger.
- Whether the local dashboard can be started and opened.

The output should use runtime names familiar to the user and provide runtime-specific guidance.
For example:

```text
Detected runtimes

  Claude Code     telemetry configured; restart required
  Codex CLI       telemetry configured
  opencode        hooks not installed
  Devin CLI       hooks not installed
```

### Offer to configure detected runtimes

For every supported runtime that needs configuration, the flow should explain what Beacon can add
and ask for confirmation. For example:

```text
opencode is installed, but Beacon hooks are not configured.
Install opencode hooks now? [Y/n]
```

If accepted, Beacon should perform the same supported operation exposed by its existing hook
commands. If a runtime requires a restart or an action Beacon cannot perform safely, the flow
should state that explicitly and add it to the final task list.

The user should not need to know in advance:

- Which runtimes use hooks versus OpenTelemetry.
- Which `--harness` value corresponds to a detected product.
- Which configuration file is involved.
- Whether a runtime restart is required.

Those are details Beacon can discover and translate into an actionable setup.

### Prove that collection works

After configuration, the getting-started flow should verify the local chain:

1. The collector service is loaded and running.
2. The OTLP endpoints are reachable.
3. Detected runtimes are configured.
4. The runtime log is writable.
5. At least an installation or validation event can be observed.

If real runtime activity is still needed, Beacon should say exactly what to do:

```text
To verify Claude Code telemetry:
  1. Restart Claude Code.
  2. Start a session and run a simple command.
  3. Return here and press Enter to check for the event.
```

### End with a concise task list

The final output should give the user a clear lay of the land rather than simply ending after the
last mutation. For example:

```text
Beacon setup summary

  ✓ Collector is running
  ✓ Logout persistence is enabled
  ✓ Codex CLI telemetry is configured
  ✓ opencode hooks are installed
  ! Restart Claude Code to load its telemetry settings
  ! Generate one agent event to complete validation

Next steps:
  1. Restart Claude Code.
  2. Use an agent normally.
  3. Open the dashboard: beacon endpoint dashboard --open
```

This summary should be safe to rerun. A second `beacon getting-started` invocation should recognize
completed steps and focus only on remaining work.

## Make the dashboard the destination

The installation and hook setup are means to an end: the user wants to see what Beacon captured.
Onboarding should therefore conclude by offering to launch the dashboard:

```text
Open the local Beacon dashboard now? [Y/n]
```

If accepted, Beacon should start the local dashboard and open the browser using the existing
dashboard behavior. If a browser cannot be opened, it should print the local URL prominently.

The final output should also preserve commands the user can return to later:

```bash
beacon endpoint status
beacon endpoint doctor
beacon endpoint dashboard --open
```

## Recommended end-to-end experience

An ideal first run would look like this:

```text
$ <copy-and-paste install command>

✓ Installer verified
✓ Beacon release archive verified
✓ Beacon binaries installed
✓ Collector service running
✓ Logout persistence enabled

Beacon is installed. Run guided setup now? [Y/n]

Detected Claude Code, Codex CLI, opencode, and Devin CLI.
Configure recommended telemetry and hooks? [Y/n]

✓ Claude Code telemetry configured
✓ Codex CLI telemetry configured
✓ opencode hooks installed
✓ Devin CLI hooks installed

Restart required: Claude Code
Open the local dashboard now? [Y/n]
```

At every point, Enter should choose the recommended safe default. Advanced users should still be
able to decline individual steps or run the underlying commands directly.

## Prototype implemented on the feature branch

A Go-native prototype is now implemented on `feature/linux-install-runner` in commit `5df7913`.
It demonstrates the revised direction rather than putting the workflow in a shell installer.

The prototype includes:

- A root-level `beacon getting-started` command with `beacon start` as an alias.
- A default-yes offer to launch guided setup after an interactive user endpoint install.
- Noninteractive detection so package and CI installs remain prompt-free.
- Collector-service and OTLP receiver health reporting.
- Existing endpoint diagnostics, including exact remediation actions.
- Runtime discovery using Beacon's current harness inventory.
- Detection of missing hook integrations for opencode, Cursor, Devin CLI, Devin Desktop, Hermes,
  Factory, and Antigravity.
- A default-yes offer to install recommended hooks.
- Per-runtime error reporting with an exact manual retry command.
- A final task list covering runtime restarts, event generation, doctor, the runtime log, and the
  dashboard.
- A default-yes offer to start and open the local dashboard.
- `--yes`, `--no-hooks`, and `--no-dashboard` controls.

Running the prototype against the development machine produced a useful immediate inventory:

```text
Beacon getting started
======================

Endpoint health
  ✓ Collector service is running
  ✓ OTLP receivers are ready
  ✓ Runtime log contains events

Detected runtimes
  Claude Code        telemetry=enabled
  Codex CLI          telemetry=enabled
  opencode           telemetry=missing
  Devin Desktop      telemetry=missing
```

This is the core experience the product should develop further: start from observed local state,
perform safe recommended actions, and leave the user with a short list of concrete remaining work.

### Prototype validation

The prototype currently has deterministic tests covering:

- Noninteractive output and recommended commands.
- Default-yes prompt behavior.
- Recommended hook installation.
- Dashboard startup handoff.
- Hook-install failure reporting and remediation.
- The post-install offer to run guided setup.

The full CLI test suite and endpoint race suite pass with the prototype. The shell-installer
prototype and its dedicated CI job were removed; documentation returned to direct, verified
release-archive installation.

### Follow-up opportunities

The current prototype is a strong integration skeleton. A production pass should consider:

- Persisting guided-setup progress so the summary can distinguish newly completed work from work
  completed during earlier runs.
- Waiting for a real event from each newly configured runtime and confirming it interactively.
- Giving each runtime a richer explanation of what was configured and whether a restart is needed.
- Detecting an already-running dashboard and opening it instead of attempting a second listener.
- Adding structured or JSON output for fleet troubleshooting without making the guided mode feel
  like a diagnostic dump.
- Ensuring terminal styling remains readable with `NO_COLOR`, redirected output, and narrow
  terminals.
- Deciding whether guided setup failures should produce a nonzero exit after still allowing the
  user to open the dashboard and inspect partial results.

## Suggested acceptance criteria

### Installer

- One documented copy-and-paste flow installs Beacon on supported Linux architectures.
- The installer itself is verified before execution.
- The selected release archive is verified before extraction.
- Unsupported platforms and architectures fail before modifying the machine.
- Missing prerequisites produce an exact remediation command.
- Linger handling never emits unexplained `pkttyagent` noise.
- A success message is printed only after post-install state checks pass.
- Noninteractive and automated installation remains possible.

### Guided setup

- `beacon getting-started` is safe and useful on both new and existing installations.
- Installation offers to run it, defaulting to yes on an interactive terminal.
- It detects supported installed runtimes and their current Beacon configuration.
- It offers to install or repair recommended runtime integrations.
- It identifies required restarts and manual actions.
- It verifies collector, service, configuration, log, and event health.
- It ends with a prioritized list of incomplete tasks.
- It offers to open the local dashboard.

### User experience

- No successful-looking transcript contains an unexplained failure.
- No detected problem is reported without a next action.
- The user never needs to infer which Beacon subcommand to run next.
- The user reaches a visible event or dashboard as part of onboarding, not through separate
  documentation discovery.

## Product principle

Beacon onboarding should optimize for time to first visible event. Installation is not complete
when binaries exist or a service starts; it is complete when the user understands what was
configured, knows what remains, and can see evidence that Beacon is working.
