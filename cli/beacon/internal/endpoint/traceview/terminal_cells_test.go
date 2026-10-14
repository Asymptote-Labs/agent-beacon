package traceview

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestTraceTerminalCellTruncation(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		width       int
		want        string
	}{
		{"wide", strings.Repeat("界", 15), 20, strings.Repeat("界", 9) + "…"},
		{"emoji-clusters", "👩‍💻👩‍💻👩‍💻", 5, "👩‍💻👩‍💻…"},
		{"combining", "ééé", 3, "ééé"},
		{"ascii", "abcdefgh", 5, "abcd…"},
		{"newlines", "one\ntwo", 20, "one two"},
		{"single-cell", "界", 1, "…"},
		{"unbounded", "界", 0, "界"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.input, tc.width)
			if got != tc.want {
				t.Fatalf("title=%q want=%q", got, tc.want)
			}
			if tc.width > 0 && ansi.StringWidth(got) > tc.width {
				t.Fatalf("rendered title exceeds terminal width: %q", got)
			}
		})
	}
}

func TestTraceTerminalCellWrapping(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		width       int
		want        []string
	}{
		{"wide", strings.Repeat("界", 15), 20, []string{strings.Repeat("界", 10), strings.Repeat("界", 5)}},
		{"emoji-clusters", "👩‍💻👩‍💻👩‍💻", 4, []string{"👩‍💻👩‍💻", "👩‍💻"}},
		{"combining", "ééé", 2, []string{"éé", "é"}},
		{"ascii-spaces", "ab  cd", 3, []string{"ab ", " cd"}},
		{"blank-crlf", "ab\r\n\ncd\r", 3, []string{"ab", "", "cd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapLines(tc.input, tc.width)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") || len(got) != len(tc.want) {
				t.Fatalf("lines=%q want=%q", got, tc.want)
			}
			for _, line := range got {
				if ansi.StringWidth(line) > tc.width {
					t.Fatalf("line exceeds terminal width: %q", line)
				}
			}
		})
	}
}

func TestTraceViewCJKRowsStayWithinTerminal(t *testing.T) {
	model := NewModel(nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	model = updated.(Model)
	model.loading = false
	model.traces = []dashboard.TraceSummaryV1{{ID: "trace", Title: strings.Repeat("界", 40)}}
	model.totalMatched = 1
	view := model.View()
	if !strings.Contains(ansi.Strip(view), "界") {
		t.Fatal("list view omitted the CJK title")
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(line), "界") && ansi.StringWidth(line) > 80 {
			t.Fatalf("list row exceeds 80 columns: width=%d %q", ansi.StringWidth(line), line)
		}
	}
}
