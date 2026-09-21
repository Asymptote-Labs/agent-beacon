package onboarding

import (
	"errors"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
)

var ErrWizardCancelled = errors.New("onboarding cancelled")

type WizardOptions struct {
	SignedIn          bool
	Email             string
	OfferManaged      bool
	DestinationOnly   bool
	PresetDestination string
	PresetPrivacyMode string
}

type WizardResult struct {
	NeedLogin   bool
	Completed   bool
	Destination string
	PrivacyMode string
}

type wizardScreen int

const (
	welcomeScreen wizardScreen = iota
	signInScreen
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
	case tea.KeyMsg:
		switch msg.String() {
		// esc and ctrl+c cancel; "q" deliberately does not. It used to, on every
		// screen, so a user typing q at the destination list killed the install --
		// and no screen could ever take free text while a bare letter meant quit.
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.screen == destinationScreen || m.screen == privacyScreen {
				m.selected = (m.selected - 1 + m.choiceCount()) % m.choiceCount()
			}
		case "down", "j":
			if m.screen == destinationScreen || m.screen == privacyScreen {
				m.selected = (m.selected + 1) % m.choiceCount()
			}
		case "enter":
			return m.advance()
		}
	}
	return m, nil
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
		m.result.NeedLogin = true
		return m, tea.Quit
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
	if m.screen == privacyScreen {
		return len(managedprivacy.Modes)
	}
	return len(m.destinations())
}

func (m wizardModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	contentWidth := min(76, max(30, width-8))
	var title, body string
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
		body = wizardWarn.Render("Nothing is forwarded by signing in or completing this wizard.") +
			"\n\nAfter installation, run `beacon endpoint connect`. Once connected, a local Vector forwarder sends new events to beacon.sh over HTTPS." +
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
		title = "Ready to install"
		label, _ := destinationCopy(m.result.Destination)
		body = "Account: " + m.options.Email + "\nDestination: " + label
		if m.result.Destination == DestinationAsymptote {
			body += "\nPrivacy: " + managedprivacy.Label(m.result.PrivacyMode) +
				"\n\nNext step after install: beacon endpoint connect"
		} else {
			body += "\n\nYour telemetry stays on this machine."
		}
	}

	card := wizardAccent.Render("B E A C O N") + "\n\n" +
		wizardAccent.Render(title) + "\n\n" +
		lipgloss.NewStyle().Width(contentWidth).Render(body) + "\n\n" +
		wizardDim.Render(wizardHint(m.screen))
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

func wizardHint(screen wizardScreen) string {
	switch screen {
	case destinationScreen:
		return "↑/↓ choose · enter continue · esc cancel"
	case privacyScreen:
		return "↑/↓ choose · enter continue · esc cancel"
	case signInScreen:
		return "enter open beacon.sh · esc cancel"
	case confirmScreen:
		return "enter install · esc cancel"
	default:
		return "enter continue · esc cancel"
	}
}
