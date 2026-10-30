package asymptoteobserve

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tail scan skips lines whose bytes rule them out without decoding them. This holds that
// shortcut to the answers decoding every line gives.

// isDuplicateEndpointEventReference is the tail scan without its shortcut: every line decoded.
func isDuplicateEndpointEventReference(path string, candidateLine []byte, window time.Duration) bool {
	decode := endpointDedupeCandidate
	candidate, ok := decode(candidateLine)
	if !ok {
		return false
	}
	data, err := readEndpointDedupeTail(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if existing, ok := decode([]byte(strings.TrimSpace(line))); ok && duplicateEndpointEvents(existing, candidate, endpointDedupeWindow(candidate.action, window)) {
			return true
		}
	}
	return false
}

func TestDedupeShortcutsNeverChangeTheAnswer(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	base := time.Date(2026, 10, 12, 3, 47, 36, 0, time.UTC)
	sessions := []interface{}{"abc-123", "other", "ABC-123", "abc-123 ", 42.0, "a\"b", "séance", "<x>"}
	actions := []string{"tool.invoked", "TOOL.INVOKED", "command.executed", "tool.completed", "file.read", "tool.İnvoked", "tool.invoKed"}
	harnesses := []string{"claude_code", "codex", "Claude_Code"}
	line := func() []byte {
		event := map[string]interface{}{
			"event":     map[string]interface{}{"action": actions[r.Intn(len(actions))]},
			"session":   map[string]interface{}{"id": sessions[r.Intn(len(sessions))], "working_directory": "/repo"},
			"harness":   map[string]interface{}{"name": harnesses[r.Intn(len(harnesses))]},
			"timestamp": base.Add(time.Duration(r.Intn(20)) * time.Second).Format(time.RFC3339Nano),
			"tool":      map[string]interface{}{"name": []string{"Bash", "Read"}[r.Intn(2)]},
			"command":   map[string]interface{}{"command": "ls"},
			"file":      map[string]interface{}{"path": "/repo/a.go"},
			"message":   []string{"m", "<html>&", "a/b"}[r.Intn(3)],
		}
		if r.Intn(2) == 0 {
			event["gen_ai"] = map[string]interface{}{"tool": map[string]interface{}{"call": map[string]interface{}{"id": fmt.Sprintf("toolu_%d", r.Intn(3))}}}
		}
		data, _ := json.Marshal(event)
		return data
	}
	dir := t.TempDir()
	answers := map[bool]int{}
	for round := 0; round < 60; round++ {
		var log strings.Builder
		for i := r.Intn(40); i >= 0; i-- {
			log.Write(line())
			log.WriteByte('\n')
		}
		path := filepath.Join(dir, fmt.Sprintf("runtime-%d.jsonl", round))
		if err := os.WriteFile(path, []byte(log.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			candidate := line()
			got := IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow)
			if want := isDuplicateEndpointEventReference(path, candidate, EndpointDuplicateWindow); got != want {
				t.Fatalf("candidate %s against\n%s\nis a duplicate: %v, decoded whole: %v", candidate, log.String(), got, want)
			}
			answers[got]++
		}
	}
	if answers[true] < 50 || answers[false] < 50 {
		t.Fatalf("the corpus has to produce both answers to test anything: %v", answers)
	}
}
