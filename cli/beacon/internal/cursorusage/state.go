package cursorusage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// State tracks, per composer (conversation), which bubbles have already been
// emitted so repeated syncs are idempotent. Bubbles carry no trustworthy
// updated-at watermark, so per-bubble emitted sets are the dedup mechanism.
type State struct {
	Composers map[string]*ComposerState `json:"composers"`
}

// ComposerState records emitted bubble ids for one composer.
type ComposerState struct {
	Emitted map[string]bool `json:"emitted"`
}

// LoadState reads state from path. A missing file yields empty state. An
// empty path yields empty, non-persisted state.
func LoadState(path string) (*State, error) {
	s := &State{Composers: map[string]*ComposerState{}}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Composers == nil {
		s.Composers = map[string]*ComposerState{}
	}
	return s, nil
}

// Save writes state to path atomically. A nil/empty path is a no-op.
func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *State) emitted(composerID, bubbleID string) bool {
	cs := s.Composers[composerID]
	return cs != nil && cs.Emitted[bubbleID]
}

func (s *State) markEmitted(composerID, bubbleID string) {
	cs := s.Composers[composerID]
	if cs == nil {
		cs = &ComposerState{Emitted: map[string]bool{}}
		s.Composers[composerID] = cs
	}
	if cs.Emitted == nil {
		cs.Emitted = map[string]bool{}
	}
	cs.Emitted[bubbleID] = true
}

// RebuildFromLog reseeds emitted bubble ids from previously synced events in
// the runtime JSONL, bounding re-emission after state-file loss to events
// already rotated out of the log. Only this package's own events (matched on
// raw.metric_name) are considered.
func (s *State) RebuildFromLog(logPath string) error {
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), writer.MaxEventBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event struct {
			Raw struct {
				MetricName string `json:"metric_name"`
				Cursor     struct {
					ComposerID string `json:"composer_id"`
					BubbleID   string `json:"bubble_id"`
				} `json:"cursor"`
			} `json:"raw"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Raw.MetricName != MetricName {
			continue
		}
		if event.Raw.Cursor.ComposerID == "" || event.Raw.Cursor.BubbleID == "" {
			continue
		}
		s.markEmitted(event.Raw.Cursor.ComposerID, event.Raw.Cursor.BubbleID)
	}
	return scanner.Err()
}
