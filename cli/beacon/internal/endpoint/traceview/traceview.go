// Package traceview provides the full-screen, local-only trace browser.
package traceview

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
)

const loadLimit = 2000

type Store interface {
	List(query string) (dashboard.TraceListResultV1, error)
	Show(id string, eventTypes []string) (dashboard.TraceShowResultV1, bool, error)
}

type localStore struct {
	logPath string
}

func (s localStore) List(query string) (dashboard.TraceListResultV1, error) {
	return dashboard.ReadTraceList(s.logPath, dashboard.TraceQuery{
		EventQuery: dashboard.EventQuery{Q: query},
		Limit:      loadLimit,
	})
}

func (s localStore) Show(id string, eventTypes []string) (dashboard.TraceShowResultV1, bool, error) {
	return dashboard.ShowTrace(s.logPath, id, dashboard.TraceQuery{
		Limit:      loadLimit,
		EventTypes: eventTypes,
	})
}

type viewMode int

const (
	listMode viewMode = iota
	detailMode
)

type listLoaded struct {
	result dashboard.TraceListResultV1
	err    error
}

type detailLoaded struct {
	result dashboard.TraceShowResultV1
	ok     bool
	err    error
}

type Model struct {
	store Store

	width, height int
	mode          viewMode
	loading       bool
	err           error

	query        string
	searchInput  string
	searching    bool
	traces       []dashboard.TraceSummaryV1
	totalMatched int
	selected     int
	listOffset   int

	detail       dashboard.TraceShowResultV1
	eventTypes   []string
	filterLabel  string
	eventIndex   int
	detailScroll int
}

var (
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("237")).Bold(true)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	ruleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func NewModel(store Store) Model {
	return Model{store: store, loading: true, filterLabel: "all"}
}

func Run(logPath string, in io.Reader, out io.Writer) error {
	model := NewModel(localStore{logPath: logPath})
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(in), tea.WithOutput(out))
	_, err := program.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return m.loadList()
}

func (m Model) loadList() tea.Cmd {
	query := m.query
	return func() tea.Msg {
		result, err := m.store.List(query)
		return listLoaded{result: result, err: err}
	}
}

func (m Model) loadDetail() tea.Cmd {
	if len(m.traces) == 0 {
		return nil
	}
	id := m.traces[m.selected].ID
	eventTypes := append([]string(nil), m.eventTypes...)
	return func() tea.Msg {
		result, ok, err := m.store.Show(id, eventTypes)
		return detailLoaded{result: result, ok: ok, err: err}
	}
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		return m, nil
	case listLoaded:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.traces = msg.result.Traces
			m.totalMatched = msg.result.TotalMatched
			m.selected, m.listOffset = 0, 0
		}
		return m, nil
	case detailLoaded:
		m.loading = false
		m.err = msg.err
		if msg.err == nil && !msg.ok {
			m.err = fmt.Errorf("trace is no longer available")
		}
		if m.err == nil {
			m.detail = msg.result
			m.eventIndex, m.detailScroll = 0, 0
		}
		return m, nil
	case tea.KeyMsg:
		if m.searching {
			return m.updateSearch(msg)
		}
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
		if m.mode == detailMode {
			return m.updateDetail(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searching = false
		m.searchInput = m.query
	case "enter":
		m.searching = false
		m.query = strings.TrimSpace(m.searchInput)
		m.loading = true
		m.err = nil
		return m, m.loadList()
	case "backspace":
		if len(m.searchInput) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.searchInput)
			m.searchInput = m.searchInput[:len(m.searchInput)-size]
		}
	default:
		if len(msg.Runes) > 0 {
			m.searchInput += string(msg.Runes)
		}
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.selected > 0 {
			m.selected--
			m.ensureVisible()
		}
	case "down", "j":
		if m.selected+1 < len(m.traces) {
			m.selected++
			m.ensureVisible()
		}
	case "pgup":
		m.selected -= m.visibleTraceCount()
		if m.selected < 0 {
			m.selected = 0
		}
		m.ensureVisible()
	case "pgdown":
		m.selected += m.visibleTraceCount()
		if m.selected >= len(m.traces) {
			m.selected = len(m.traces) - 1
		}
		m.ensureVisible()
	case "enter":
		if len(m.traces) > 0 {
			m.mode = detailMode
			m.loading = true
			m.err = nil
			return m, m.loadDetail()
		}
	case "/":
		m.searching = true
		m.searchInput = m.query
	case "c":
		if m.query != "" {
			m.query = ""
			m.searchInput = ""
			m.loading = true
			return m, m.loadList()
		}
	case "r":
		m.loading = true
		m.err = nil
		return m, m.loadList()
	}
	return m, nil
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace":
		m.mode = listMode
		m.err = nil
	case "up", "k":
		if m.eventIndex > 0 {
			m.eventIndex--
			m.detailScroll = 0
		}
	case "down", "j":
		if m.eventIndex+1 < len(m.detail.Events) {
			m.eventIndex++
			m.detailScroll = 0
		}
	case "pgup", "ctrl+u":
		m.detailScroll -= m.detailPageHeight()
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
	case "pgdown", "ctrl+d":
		m.detailScroll += m.detailPageHeight()
		m.clampDetailScroll()
	case "1":
		return m.setFilter("all", nil)
	case "2":
		return m.setFilter("messages", []string{"user_message", "agent_message", "agent_reasoning"})
	case "3":
		return m.setFilter("tools", []string{"tool_call", "tool_result", "command", "file", "mcp"})
	case "4":
		return m.setFilter("errors", []string{"error"})
	case "r":
		m.loading = true
		m.err = nil
		return m, m.loadDetail()
	}
	return m, nil
}

func (m Model) setFilter(label string, eventTypes []string) (tea.Model, tea.Cmd) {
	m.filterLabel = label
	m.eventTypes = eventTypes
	m.loading = true
	m.err = nil
	return m, m.loadDetail()
}

func (m *Model) ensureVisible() {
	count := m.visibleTraceCount()
	if m.selected < m.listOffset {
		m.listOffset = m.selected
	}
	if m.selected >= m.listOffset+count {
		m.listOffset = m.selected - count + 1
	}
	if m.listOffset < 0 {
		m.listOffset = 0
	}
}

func (m *Model) clampDetailScroll() {
	if len(m.detail.Events) == 0 {
		m.detailScroll = 0
		return
	}
	lines := eventDetails(m.detail.Events[m.eventIndex], max(20, m.width-4))
	maxScroll := max(0, len(lines)-m.detailPageHeight())
	if m.detailScroll > maxScroll {
		m.detailScroll = maxScroll
	}
}

func (m Model) visibleTraceCount() int {
	// Reserve a possible date heading for every visible trace so traces from
	// different days never make the view taller than the terminal.
	count := (m.height - 7) / 3
	if count < 1 {
		return 1
	}
	return count
}

func (m Model) detailPageHeight() int {
	height := m.height - 16
	if height < 3 {
		return 3
	}
	return height
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading Beacon traces…"
	}
	if m.mode == detailMode {
		return m.detailView()
	}
	return m.listView()
}

func (m Model) listView() string {
	var b strings.Builder
	b.WriteString("  " + accentStyle.Render("B E A C O N   T R A C E S") + "\n")
	b.WriteString("  " + ruleStyle.Render(strings.Repeat("─", max(1, m.width-4))) + "\n")
	if m.searching {
		b.WriteString("  Search › " + m.searchInput + "█\n")
	} else if m.query != "" {
		b.WriteString(fmt.Sprintf("  Search: %s  %s\n", accentStyle.Render(m.query), dimStyle.Render("(c clear)")))
	} else {
		b.WriteString("  " + dimStyle.Render("Local endpoint traces · nothing is sent anywhere") + "\n")
	}
	if m.loading {
		b.WriteString("\n  Loading traces…\n")
		return b.String()
	}
	if m.err != nil {
		b.WriteString("\n  " + errorStyle.Render(m.err.Error()) + "\n")
		b.WriteString("\n  " + dimStyle.Render("r retry · q quit") + "\n")
		return b.String()
	}
	if len(m.traces) == 0 {
		b.WriteString("\n  No local traces found.\n")
		b.WriteString("  " + dimStyle.Render("Generate agent activity or press / to change the search.") + "\n")
		return b.String()
	}
	end := min(len(m.traces), m.listOffset+m.visibleTraceCount())
	lastDate := ""
	for i := m.listOffset; i < end; i++ {
		trace := m.traces[i]
		date, clock := displayTime(trace.UpdatedAt)
		if date != lastDate {
			b.WriteString("  " + dimStyle.Render(date) + "\n")
			lastDate = date
		}
		titleWidth := max(12, m.width-35)
		line := fmt.Sprintf("%-8s  %-*s  %s", clock, titleWidth, truncate(trace.Title, titleWidth), trace.Harness.Name)
		meta := fmt.Sprintf("           %s · %d events · %s", tracePath(trace), trace.EventCount, contentLabel(trace))
		if i == m.selected {
			b.WriteString(selectedStyle.Render(fit("  ❯ "+line, m.width)) + "\n")
			b.WriteString(selectedStyle.Render(fit("    "+meta, m.width)) + "\n")
		} else {
			b.WriteString(fit("    "+line, m.width) + "\n")
			b.WriteString(dimStyle.Render(fit("    "+meta, m.width)) + "\n")
		}
	}
	b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("%d traces · ↑/↓ move · enter open · / search · r refresh · q quit", m.totalMatched)) + "\n")
	return b.String()
}

func (m Model) detailView() string {
	var b strings.Builder
	trace := m.detail.Trace
	title := trace.Title
	if title == "" && len(m.traces) > 0 {
		title = m.traces[m.selected].Title
	}
	b.WriteString("  " + accentStyle.Render(truncate(title, max(20, m.width-4))) + "\n")
	b.WriteString("  " + dimStyle.Render(fmt.Sprintf("%s · %s · %s%s · filter: %s",
		trace.Harness.Name, tracePath(trace), trace.UpdatedAt, traceUsageLabel(trace), m.filterLabel)) + "\n")
	b.WriteString("  " + ruleStyle.Render(strings.Repeat("─", max(1, m.width-4))) + "\n")
	if m.loading {
		b.WriteString("\n  Loading events…\n")
		return b.String()
	}
	if m.err != nil {
		b.WriteString("\n  " + errorStyle.Render(m.err.Error()) + "\n")
		return b.String()
	}
	if len(m.detail.Events) == 0 {
		b.WriteString("\n  No events match this filter.\n")
	} else {
		start, end := eventWindow(m.eventIndex, len(m.detail.Events), 5)
		for i := start; i < end; i++ {
			event := m.detail.Events[i]
			line := fmt.Sprintf("#%-4d %-16s %s", event.Number, event.Type, eventTitle(event))
			if i == m.eventIndex {
				b.WriteString(selectedStyle.Render(fit("  ❯ "+line, m.width)) + "\n")
			} else {
				b.WriteString(fit("    "+line, m.width) + "\n")
			}
		}
		b.WriteString("  " + ruleStyle.Render(strings.Repeat("─", max(1, m.width-4))) + "\n")
		lines := eventDetails(m.detail.Events[m.eventIndex], max(20, m.width-4))
		if m.detailScroll >= len(lines) {
			m.detailScroll = max(0, len(lines)-1)
		}
		end = min(len(lines), m.detailScroll+m.detailPageHeight())
		for _, line := range lines[m.detailScroll:end] {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n  " + dimStyle.Render("esc back · ↑/↓ event · pgup/pgdn details · 1 all · 2 messages · 3 tools · 4 errors · q quit") + "\n")
	return b.String()
}

func eventDetails(event dashboard.TraceEventV1, width int) []string {
	lines := []string{
		accentStyle.Render(fmt.Sprintf("#%d %s", event.Number, event.Type)),
		dimStyle.Render(strings.TrimSpace(event.Timestamp + "  " + event.Action)),
	}
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		lines = append(lines, accentStyle.Render(label))
		lines = append(lines, wrapLines(value, width)...)
	}
	add("Summary", firstNonEmpty(contentText(event.Content), event.Summary))
	if event.Tool != nil {
		add("Tool", strings.TrimSpace(strings.Join([]string{event.Tool.Name, event.Tool.Command, event.Tool.Path}, " ")))
		addJSON := func(label string, value any) {
			if value == nil {
				return
			}
			data, _ := json.MarshalIndent(value, "", "  ")
			add(label, string(data))
		}
		addJSON("Arguments", event.Tool.Arguments)
		addJSON("Result", event.Tool.Result)
	}
	if event.Command != nil {
		add("Command", event.Command.Command)
		if event.Command.Output != nil {
			add("Output", event.Command.Output.Text)
		}
	}
	if event.File != nil {
		add("File", strings.TrimSpace(event.File.Operation+" "+event.File.Path))
		if event.File.Diff != nil {
			add("Diff", event.File.Diff.Text)
		}
	}
	if event.MCP != nil {
		add("MCP", strings.TrimSpace(strings.Join([]string{event.MCP.Server, event.MCP.Tool, event.MCP.Method, event.MCP.ResourceURI}, " ")))
	}
	if event.Approval != nil {
		add("Approval", strings.TrimSpace(event.Approval.Decision+" "+event.Approval.Reason))
	}
	if event.Usage != nil {
		add("Usage", fmt.Sprintf("input %d · output %d · cache read %d · cost $%.6f",
			event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CacheReadInputTokens, event.Usage.CostUSD))
	}
	return lines
}

func contentText(content *dashboard.TraceContentV1) string {
	if content == nil {
		return ""
	}
	return content.Text
}

func eventTitle(event dashboard.TraceEventV1) string {
	return truncate(firstNonEmpty(event.Title, event.Summary, contentText(event.Content), event.Action), 100)
}

func eventWindow(selected, total, size int) (int, int) {
	if total <= size {
		return 0, total
	}
	start := selected - size/2
	if start < 0 {
		start = 0
	}
	if start+size > total {
		start = total - size
	}
	return start, start + size
}

func displayTime(raw string) (string, string) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "Unknown date", "--:--"
	}
	local := parsed.Local()
	return local.Format("Mon, Jan 2"), local.Format("3:04 PM")
}

func tracePath(trace dashboard.TraceSummaryV1) string {
	if trace.Session != nil && trace.Session.WorkingDirectory != "" {
		return trace.Session.WorkingDirectory
	}
	if trace.Repository != nil && trace.Repository.Path != "" {
		return trace.Repository.Path
	}
	return "local"
}

func contentLabel(trace dashboard.TraceSummaryV1) string {
	label := trace.Content.Retention
	if label == "" {
		label = "metadata"
	}
	if trace.Content.HasRedactions {
		label += ", redacted"
	}
	if trace.Content.HasTruncations {
		label += ", truncated"
	}
	return label
}

func traceUsageLabel(trace dashboard.TraceSummaryV1) string {
	if trace.TokenUsage == nil {
		return ""
	}
	total := trace.TokenUsage.InputTokens + trace.TokenUsage.OutputTokens
	if trace.TokenUsage.CostUSD > 0 {
		return fmt.Sprintf(" · %d tokens · $%.4f", total, trace.TokenUsage.CostUSD)
	}
	return fmt.Sprintf(" · %d tokens", total)
}

func wrapLines(value string, width int) []string {
	var out []string
	for _, raw := range strings.Split(value, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			out = append(out, "")
			continue
		}
		for len([]rune(line)) > width {
			runes := []rune(line)
			out = append(out, string(runes[:width]))
			line = string(runes[width:])
		}
		out = append(out, line)
	}
	return out
}

func fit(value string, width int) string {
	return truncate(value, max(1, width))
}

func truncate(value string, width int) string {
	runes := []rune(strings.ReplaceAll(value, "\n", " "))
	if width <= 0 || len(runes) <= width {
		return string(runes)
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
