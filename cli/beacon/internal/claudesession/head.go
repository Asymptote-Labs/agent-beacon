package claudesession

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// Bounds for reading the head of a transcript. Listing opens every transcript, so it reads only
// enough to find where the session ran and what it was about.
const (
	headMaxLines    = 64
	headMaxBytes    = 4 << 20
	headMaxLineSize = 1 << 20
)

// Head is what the start of a transcript records about its session.
type Head struct {
	CWD         string
	GitBranch   string
	FirstPrompt string
}

// ReadHead reads where a Claude session ran, its branch and its first prompt from the start of its
// transcript. Claude names each project directory after its path with every separator turned into
// a dash, which cannot be decoded back when the path itself contains one, so a session without an
// index entry needs the cwd its transcript records. A missing or unreadable file yields an empty
// Head.
func ReadHead(path string) Head {
	var head Head
	f, err := sessionfs.Open(path)
	if err != nil {
		return head
	}
	defer f.Close()
	reader := bufio.NewReaderSize(io.LimitReader(f, headMaxBytes), 64<<10)
	for range headMaxLines {
		line, status, _ := readLine(reader, headMaxLineSize)
		if status == lineOK || status == linePartial {
			head.consume(line)
			if head.CWD != "" && head.GitBranch != "" && head.FirstPrompt != "" {
				return head
			}
		}
		if status != lineOK && status != lineOversized {
			return head
		}
	}
	return head
}

func (h *Head) consume(line []byte) {
	var entry struct {
		Type      string `json:"type"`
		CWD       string `json:"cwd"`
		GitBranch string `json:"gitBranch"`
		IsMeta    bool   `json:"isMeta"`
		Message   struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil {
		return
	}
	if h.CWD == "" && strings.TrimSpace(entry.CWD) != "" {
		h.CWD = entry.CWD
	}
	if h.GitBranch == "" {
		h.GitBranch = entry.GitBranch
	}
	if h.FirstPrompt == "" && entry.Type == "user" && !entry.IsMeta && entry.Message.Role == "user" {
		h.FirstPrompt = promptText(entry.Message.Content)
	}
}

// promptText returns the text a person typed, skipping tool results and the tagged blocks Claude
// injects for slash commands and local command output.
func promptText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &blocks) != nil {
			return ""
		}
		for _, block := range blocks {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				text = block.Text
				break
			}
		}
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<") {
		return ""
	}
	return text
}
