package handoff

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// Bounds for reading the head of a Claude transcript while listing. A listing opens every
// transcript, so it reads only enough to find where the session ran and what it was about.
const (
	claudeHeadMaxLines    = 64
	claudeHeadMaxBytes    = 4 << 20
	claudeHeadMaxLineSize = 1 << 20
)

type claudeHead struct {
	CWD         string
	GitBranch   string
	FirstPrompt string
}

// readClaudeHead reads where a Claude session ran and its first prompt from the start of its
// transcript. Claude names each project directory after its path with every separator turned into
// a dash, which cannot be decoded back when the path itself contains one, so a session without an
// index entry needs the cwd its transcript records.
func readClaudeHead(path string) claudeHead {
	f, err := os.Open(path)
	if err != nil {
		return claudeHead{}
	}
	defer f.Close()

	var head claudeHead
	reader := bufio.NewReaderSize(io.LimitReader(f, claudeHeadMaxBytes), 64<<10)
	for lines := 0; lines < claudeHeadMaxLines; lines++ {
		line, err := readBoundedLine(reader, claudeHeadMaxLineSize)
		if len(line) > 0 {
			head.consume(line)
		}
		if head.CWD != "" && head.GitBranch != "" && head.FirstPrompt != "" {
			break
		}
		if err != nil {
			break
		}
	}
	return head
}

func (h *claudeHead) consume(line []byte) {
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
	if h.CWD == "" {
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

// readBoundedLine returns the next line without its newline. A line longer than max is skipped
// whole and returned empty, so one huge tool result cannot stall a listing.
func readBoundedLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	oversized := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !oversized {
			if len(line)+len(chunk) > max {
				oversized = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if oversized {
			return nil, err
		}
		return []byte(strings.TrimRight(string(line), "\r\n")), err
	}
}
