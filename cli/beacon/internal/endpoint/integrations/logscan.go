package integrations

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"
)

func HasRecentHarnessEvent(logPath, harnessName string) bool {
	_, ok := LastHarnessEvent(logPath, harnessName)
	return ok
}

func HasHarnessEventSince(logPath, harnessName string, since time.Time) bool {
	last, ok := LastHarnessEvent(logPath, harnessName)
	if !ok || last.IsZero() {
		return false
	}
	return !last.Before(since)
}

func LastHarnessEvent(logPath, harnessName string) (time.Time, bool) {
	if logPath == "" || strings.TrimSpace(harnessName) == "" {
		return time.Time{}, false
	}
	file, err := os.Open(logPath)
	if err != nil {
		return time.Time{}, false
	}
	defer file.Close()

	want := strings.ToLower(strings.TrimSpace(harnessName))
	var last time.Time
	found := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if harness, ok := event["harness"].(map[string]interface{}); ok {
			if name, _ := harness["name"].(string); strings.ToLower(strings.TrimSpace(name)) == want {
				found = true
				if ts, ok := event["timestamp"].(string); ok {
					if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil && parsed.After(last) {
						last = parsed
					}
				}
			}
		}
	}
	return last, found
}
