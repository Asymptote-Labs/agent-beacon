package onboarding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
)

var ErrWizardCancelled = errors.New("onboarding cancelled")

// Account is what a completed sign-in tells the wizard.
//
// It carries no token. This package draws a terminal and holds no credential, and
// the sign-in itself is injected precisely so that stays true.
type Account struct {
	Email string
}

// SignInPrompt is what the user needs in order to finish signing in elsewhere.
type SignInPrompt struct {
	URL        string
	WillOpen   bool
	BrowserErr error
}

// Reporter is how a sign-in in progress tells the wizard what is happening. It is
// called from the goroutine running the sign-in; the wizard funnels every call onto
// its own event channel.
type Reporter interface {
	Prompt(SignInPrompt)
}

// SignInRequest is how the wizard asks for this particular attempt. It exists so a
// retry can change its mind about the browser: the recovery screen lets a user who
// has no usable browser switch to opening the URL themselves.
type SignInRequest struct {
	NoBrowser bool
}

// SignInFunc signs in without the wizard leaving the screen. It runs off the UI
// goroutine and must return when ctx is cancelled. An error moves the wizard to its
// recovery screen; it never fails the install by itself.
type SignInFunc func(ctx context.Context, req SignInRequest, r Reporter) (Account, error)

type WizardOptions struct {
	SignedIn          bool
	Email             string
	OfferManaged      bool
	DestinationOnly   bool
	PresetDestination string
	PresetPrivacyMode string

	// SignIn signs in in place. When nil the wizard keeps its original contract:
	// it exits with NeedLogin and the caller signs in and runs it again.
	SignIn SignInFunc
	// NoBrowser presents the URL to open rather than claiming one will open.
	NoBrowser bool
	// Now is injectable so elapsed time renders deterministically in tests.
	Now func() time.Time
	// SignInTimeout is rendered as a countdown. The real deadline lives in SignIn.
	SignInTimeout time.Duration
}

type WizardResult struct {
	NeedLogin   bool
	Completed   bool
	Destination string
	PrivacyMode string

	// SignedInEmail is set when the in-wizard sign-in succeeded, so the caller can
	// record who completed setup without inspecting the session again.
	SignedInEmail string
	// WithoutAccount is true when the user finished setup without an account after
	// a sign-in failure. Destination is then always local: managed forwarding has
	// nothing to authorize it.
	WithoutAccount bool
}

type wizardScreen int

const (
	welcomeScreen wizardScreen = iota
	signInScreen
	signInWaitScreen
	signInFailedScreen
	destinationScreen
	managedDisclosureScreen
	privacyScreen
	confirmScreen
)

type wizardModel struct {
	options  WizardOptions
	result   WizardResult
	screen   wizardScreen
	selected int
	width    int
	height   int

	// events carries everything a background sign-in reports. It is buffered so a
	// reporter never blocks on a UI that has stopped reading.
	events chan tea.Msg
	// cancelSignIn stops the sign-in in flight, on esc or on quit.
	cancelSignIn context.CancelFunc
	prompt       SignInPrompt
	signInErr    error
	startedAt    time.Time
	now          time.Time
	frame        int
}

// spinnerFrames animate the wait. Braille is what every terminal capable of the
// alternate screen this wizard already requires can draw.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type tickMsg time.Time

type signInPromptMsg SignInPrompt

type signInDoneMsg struct {
	account Account
	err     error
}

type chanReporter struct{ events chan<- tea.Msg }

func (r chanReporter) Prompt(p SignInPrompt) {
	select {
	case r.events <- signInPromptMsg(p):
	default:
	}
}

func waitForEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

func tickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m wizardModel) clock() time.Time {
	if m.options.Now != nil {
		return m.options.Now()
	}
	return time.Now()
}

var (
	wizardAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
	wizardDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	wizardChoice = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("237")).Bold(true)
	wizardWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
)

func newWizardModel(options WizardOptions) wizardModel {
	screen := welcomeScreen
	result := WizardResult{Destination: options.PresetDestination, PrivacyMode: options.PresetPrivacyMode}
	if options.DestinationOnly {
		switch {
		case !options.SignedIn:
			screen = signInScreen
		case options.PresetDestination == DestinationAsymptote:
			screen = managedDisclosureScreen
		case options.PresetDestination != "":
			screen = confirmScreen
		default:
			screen = destinationScreen
		}
	}
	return wizardModel{options: options, result: result, screen: screen}
}

func RunWizard(in io.Reader, out io.Writer, options WizardOptions) (WizardResult, error) {
	model := newWizardModel(options)
	model.events = make(chan tea.Msg, 8)
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(in), tea.WithOutput(out))
	final, err := program.Run()
	if err != nil {
		return WizardResult{}, err
	}
	result := final.(wizardModel).result
	if !result.NeedLogin && !result.Completed {
		return WizardResult{}, ErrWizardCancelled
	}
	return result, nil
}

func (m wizardModel) Init() tea.Cmd {
	return nil
}

func (m wizardModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		// Only the waiting screen animates, and only it keeps the ticker alive.
		if m.screen != signInWaitScreen {
			return m, nil
		}
		m.now = time.Time(msg)
		m.frame++
		return m, tickCmd()
	case signInPromptMsg:
		m.prompt = SignInPrompt(msg)
		return m, waitForEvent(m.events)
	case signInDoneMsg:
		return m.signInFinished(msg)
	case tea.KeyMsg:
		switch msg.String() {
		// esc and ctrl+c cancel; "q" deliberately does not. It used to, on every
		// screen, so a user typing q at the destination list killed the install --
		// and no screen could ever take free text while a bare letter meant quit.
		case "ctrl+c":
			return m.cancelAndQuit()
		case "esc":
			// Abandoning a browser sign-in returns to the sign-in screen rather than
			// ending the install: the browser may simply not have opened.
			if m.screen == signInWaitScreen {
				m.stopSignIn()
				m.screen = signInScreen
				return m, nil
			}
			return m.cancelAndQuit()
		case "up", "k":
			if n := m.choiceCount(); m.isPicker() && n > 0 {
				m.selected = (m.selected - 1 + n) % n
			}
		case "down", "j":
			if n := m.choiceCount(); m.isPicker() && n > 0 {
				m.selected = (m.selected + 1) % n
			}
		case "enter":
			return m.advance()
		}
	}
	return m, nil
}

func (m wizardModel) isPicker() bool {
	return m.screen == destinationScreen || m.screen == privacyScreen || m.screen == signInFailedScreen
}

func (m *wizardModel) stopSignIn() {
	if m.cancelSignIn != nil {
		m.cancelSignIn()
		m.cancelSignIn = nil
	}
}

func (m wizardModel) cancelAndQuit() (tea.Model, tea.Cmd) {
	m.stopSignIn()
	return m, tea.Quit
}

// startSignIn runs the injected sign-in off the UI goroutine and shows the waiting
// screen. Waiting begins immediately: the URL is reported by the sign-in itself
// once its callback server is listening, never gated on the user pressing anything.
func (m wizardModel) startSignIn() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelSignIn = cancel
	m.signInErr = nil
	m.prompt = SignInPrompt{}
	m.startedAt = m.clock()
	m.now = m.startedAt
	m.frame = 0
	m.screen = signInWaitScreen

	events, signIn := m.events, m.options.SignIn
	req := SignInRequest{NoBrowser: m.options.NoBrowser}
	go func() {
		account, err := signIn(ctx, req, chanReporter{events: events})
		events <- signInDoneMsg{account: account, err: err}
	}()
	return m, tea.Batch(waitForEvent(m.events), tickCmd())
}

func (m wizardModel) signInFinished(msg signInDoneMsg) (tea.Model, tea.Cmd) {
	m.stopSignIn()
	if msg.err != nil {
		// A cancel the user asked for has already moved the screen; do not overwrite
		// it with a failure they did not cause.
		if m.screen != signInWaitScreen {
			return m, nil
		}
		m.signInErr = msg.err
		m.selected = 0
		m.screen = signInFailedScreen
		return m, nil
	}
	m.options.SignedIn = true
	m.options.Email = msg.account.Email
	m.result.SignedInEmail = msg.account.Email
	m.selected = 0
	m.screen = m.afterSignIn()
	return m, nil
}

// afterSignIn is where the wizard resumes once an account exists.
func (m wizardModel) afterSignIn() wizardScreen {
	switch {
	case m.options.PresetDestination == DestinationAsymptote:
		return managedDisclosureScreen
	case m.options.PresetDestination != "":
		return confirmScreen
	default:
		return destinationScreen
	}
}

func (m wizardModel) advance() (tea.Model, tea.Cmd) {
	switch m.screen {
	case welcomeScreen:
		switch {
		case !m.options.SignedIn:
			m.screen = signInScreen
		case m.options.PresetDestination == DestinationAsymptote:
			m.screen = managedDisclosureScreen
		case m.options.PresetDestination != "":
			m.screen = confirmScreen
		default:
			m.screen = destinationScreen
		}
	case signInScreen:
		if m.options.SignIn != nil {
			return m.startSignIn()
		}
		// No sign-in was injected: keep the original contract, where the caller
		// signs in and runs the wizard again.
		m.result.NeedLogin = true
		return m, tea.Quit
	case signInWaitScreen:
		// Enter does nothing while the browser has the floor.
		return m, nil
	case signInFailedScreen:
		return m.recover()
	case destinationScreen:
		m.result.Destination = m.destinations()[m.selected]
		if m.result.Destination == DestinationAsymptote {
			m.screen = managedDisclosureScreen
		} else {
			m.screen = confirmScreen
		}
	case managedDisclosureScreen:
		m.selected = privacyIndex(m.result.PrivacyMode)
		m.screen = privacyScreen
	case privacyScreen:
		m.result.PrivacyMode = managedprivacy.Modes[m.selected]
		m.screen = confirmScreen
	case confirmScreen:
		m.result.Completed = true
		return m, tea.Quit
	}
	return m, nil
}

func (m wizardModel) destinations() []string {
	if m.options.OfferManaged {
		return []string{DestinationAsymptote, DestinationLocal}
	}
	return []string{DestinationLocal}
}

func (m wizardModel) choiceCount() int {
	switch m.screen {
	case privacyScreen:
		return len(managedprivacy.Modes)
	case signInFailedScreen:
		return len(m.recoveryChoices())
	default:
		return len(m.destinations())
	}
}

// recoveryChoice is one way out of a sign-in that did not finish.
type recoveryChoice struct {
	id     string
	label  string
	detail string
}

// recoveryChoices exist only here, after an attempt has actually failed.
//
// Signing in is the expected path, so the sign-in screen offers nothing else; a
// visible "skip" there would read as a suggestion. But a machine that is offline,
// has no browser, or cannot receive the loopback redirect still has to be able to
// finish, or a local-first tool becomes uninstallable without tribal knowledge of
// an environment variable.
func (m wizardModel) recoveryChoices() []recoveryChoice {
	choices := []recoveryChoice{
		{id: "retry", label: "Try signing in again", detail: "Open beacon.sh and wait for the browser again."},
	}
	if m.prompt.WillOpen || m.prompt.BrowserErr != nil {
		choices = append(choices, recoveryChoice{
			id:     "manual",
			label:  "Show the URL instead of opening a browser",
			detail: "Open the address yourself, in a browser on this machine.",
		})
	}
	return append(choices,
		recoveryChoice{
			id:     "local",
			label:  "Finish setup without an account",
			detail: "Install Beacon with telemetry kept on this machine. Connect later with `beacon endpoint connect`.",
		},
		recoveryChoice{id: "cancel", label: "Cancel setup", detail: "Stop without installing."},
	)
}

func (m wizardModel) recover() (tea.Model, tea.Cmd) {
	choices := m.recoveryChoices()
	if m.selected < 0 || m.selected >= len(choices) {
		return m, nil
	}
	switch choices[m.selected].id {
	case "retry":
		return m.startSignIn()
	case "manual":
		m.options.NoBrowser = true
		return m.startSignIn()
	case "local":
		m.result.WithoutAccount = true
		m.result.Destination = DestinationLocal
		m.result.PrivacyMode = ""
		m.screen = confirmScreen
		return m, nil
	default:
		return m, tea.Quit
	}
}

func (m wizardModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	contentWidth := min(76, max(30, width-8))
	var title, body, raw string
	switch m.screen {
	case welcomeScreen:
		title = "Welcome to Beacon"
		body = "One local timeline for every AI agent.\n\nThis interactive setup signs you in, then asks whether telemetry should stay local or be forwarded to Beacon Managed."
		if m.options.SignedIn {
			body += "\n\n" + wizardDim.Render("Signed in as "+m.options.Email)
		}
	case signInScreen:
		title = "Sign in to continue"
		body = "Beacon will open beacon.sh in your browser using a secure PKCE flow.\n\nSigning in does not send telemetry, register a managed endpoint, or enable forwarding."
	case signInWaitScreen:
		title = "Waiting for you to finish signing in"
		switch {
		case m.prompt.URL == "":
			body = "Starting sign-in..."
		case m.prompt.BrowserErr != nil:
			body = "A browser could not be opened here. Open this URL on this machine:"
		case m.prompt.WillOpen:
			body = "Your browser should have opened. If it did not, open this URL:"
		default:
			body = "Open this URL to sign in:"
		}
		raw = m.prompt.URL
		if m.prompt.URL != "" {
			raw += "\n\n" + wizardDim.Render(m.waitStatus()) +
				"\n\n" + wizardDim.Render("Signing in does not send telemetry or connect this machine.")
		}
	case signInFailedScreen:
		title = "Sign-in did not finish"
		if m.signInErr != nil {
			body = wizardWarn.Render(m.signInErr.Error()) + "\n\n"
		}
		var rows []string
		for index, choice := range m.recoveryChoices() {
			line := "    " + choice.label
			if index == m.selected {
				line = wizardChoice.Render("  ❯ " + choice.label)
			}
			rows = append(rows, line, choiceDetail(choice.detail, contentWidth))
		}
		body += strings.Join(rows, "\n")
	case destinationScreen:
		title = "Where should this machine's telemetry go?"
		var rows []string
		for index, destination := range m.destinations() {
			label, detail := destinationCopy(destination)
			prefix := "    "
			line := prefix + label
			if index == m.selected {
				line = wizardChoice.Render("  ❯ " + label)
			}
			rows = append(rows, line, choiceDetail(detail, contentWidth))
		}
		body = strings.Join(rows, "\n")
	case managedDisclosureScreen:
		title = "Beacon Managed sends new telemetry to beacon.sh"
		body = wizardWarn.Render("Nothing is forwarded by signing in.") +
			"\n\nBeacon installs, then connects this machine: a device key is minted and a local Vector forwarder starts sending new events to beacon.sh over HTTPS." +
			"\n\nRuntime events recorded before you connect stay on this machine. The inventory snapshot -- which agent runtimes, skills, and MCP servers are installed here -- uploads once so the dashboard has this endpoint's baseline." +
			"\n\nStop any time with `beacon endpoint disconnect`."
	case privacyScreen:
		title = "Choose what Beacon Managed receives"
		var rows []string
		for index, mode := range managedprivacy.Modes {
			label, detail := privacyCopy(mode)
			line := "    " + label
			if index == m.selected {
				line = wizardChoice.Render("  ❯ " + label)
			}
			rows = append(rows, line, choiceDetail(detail, contentWidth))
		}
		body = strings.Join(rows, "\n")
	case confirmScreen:
		title = "Ready to set up Beacon"
		label, _ := destinationCopy(m.result.Destination)
		if m.options.Email != "" {
			body = "Account: " + m.options.Email + "\n"
		}
		body += "Destination: " + label
		if m.result.Destination == DestinationAsymptote {
			body += "\nPrivacy: " + managedprivacy.Label(m.result.PrivacyMode) +
				"\n\n" + wizardWarn.Render("Confirming installs Beacon and starts forwarding new events to beacon.sh.") +
				"\n\n" + privacySends(m.result.PrivacyMode)
		} else {
			body += "\n\nYour telemetry stays on this machine."
			if m.result.WithoutAccount {
				body += " You are not signed in; connect later with `beacon endpoint connect`."
			}
		}
	}

	card := wizardAccent.Render("B E A C O N") + "\n\n" +
		wizardAccent.Render(title) + "\n\n" +
		lipgloss.NewStyle().Width(contentWidth).Render(body)
	if raw != "" {
		// Deliberately not width-constrained: a soft-wrapped URL is one a user
		// cannot copy, and copying it is the whole point of showing it.
		card += "\n\n" + raw
	}
	card += "\n\n" + wizardDim.Render(m.hint())
	return lipgloss.NewStyle().Padding(1, 3).Render(card)
}

func destinationCopy(destination string) (string, string) {
	switch destination {
	case DestinationAsymptote:
		return "Beacon Managed (recommended)",
			"One searchable history across Claude Code, Cursor, Codex and more. " +
				"Unlimited retention, token and usage analytics. No backend to run."
	default:
		return "Local only",
			"Everything stays in ~/.beacon on this machine. You manage retention, " +
				"and history is limited to this device."
	}
}

// choiceDetail renders the dim explanation under a choice, wrapped to the card and
// indented on every line.
//
// The detail used to be emitted as one pre-indented string and wrapped later with
// the rest of the body, which indented the first line and left every continuation
// flush against the margin. That was invisible while the captions were one-liners
// and became wrong the moment they were not.
func choiceDetail(detail string, width int) string {
	const indent = "      "
	inner := max(20, width-len(indent))
	var lines []string
	for _, line := range strings.Split(lipgloss.NewStyle().Width(inner).Render(detail), "\n") {
		lines = append(lines, wizardDim.Render(indent+strings.TrimRight(line, " ")))
	}
	return strings.Join(lines, "\n")
}

func privacyCopy(mode string) (string, string) {
	if mode == managedprivacy.MetadataOnly {
		return "Metadata only", "Omit prompts, responses, reasoning, tool arguments/results, command output, raw fields, and diffs."
	}
	return "Standard (recommended)", "Send locally sanitized retained content, including prompts and tool activity."
}

func privacyIndex(mode string) int {
	for index, candidate := range managedprivacy.Modes {
		if candidate == mode {
			return index
		}
	}
	return 0
}

// privacySends states, in the present tense, what the selected mode puts on the
// wire. The confirm screen is where forwarding is authorized, so it has to name what
// leaves this machine rather than point at a command to run later.
func privacySends(mode string) string {
	if mode == managedprivacy.MetadataOnly {
		return "Metadata only: prompts, responses, reasoning, tool arguments and results, command output and diffs stay on this machine."
	}
	return "Standard: the locally sanitized event, including prompts and tool activity."
}

// waitStatus renders the spinner, how long this has been going, and how long is
// left, so a wait with no visible progress is not mistaken for a hang.
func (m wizardModel) waitStatus() string {
	frame := spinnerFrames[m.frame%len(spinnerFrames)]
	elapsed := m.now.Sub(m.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	status := fmt.Sprintf("%s  %s elapsed", frame, clockFormat(elapsed))
	if m.options.SignInTimeout > 0 {
		if remaining := m.options.SignInTimeout - elapsed; remaining > 0 {
			status += fmt.Sprintf(" · times out in %s", clockFormat(remaining))
		}
	}
	return status
}

func clockFormat(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func (m wizardModel) hint() string {
	if m.screen == confirmScreen && m.result.Destination == DestinationAsymptote {
		return "enter install and connect · esc cancel"
	}
	return wizardHint(m.screen)
}

func wizardHint(screen wizardScreen) string {
	switch screen {
	case destinationScreen:
		return "↑/↓ choose · enter continue · esc cancel"
	case privacyScreen:
		return "↑/↓ choose · enter continue · esc cancel"
	case signInScreen:
		return "enter open beacon.sh · esc cancel"
	case signInWaitScreen:
		return "esc go back · ctrl+c cancel setup"
	case signInFailedScreen:
		return "↑/↓ choose · enter continue · esc cancel"
	case confirmScreen:
		return "enter install · esc cancel"
	default:
		return "enter continue · esc cancel"
	}
}
