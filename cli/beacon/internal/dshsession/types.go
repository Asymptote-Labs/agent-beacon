// Package dshsession reads DeepSeek Harness' native session store and maps it
// into Beacon endpoint events.
package dshsession

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	Harness      = "deepseek_harness"
	StateVersion = 1

	SessionFileZstd = "session.jsonl.zstd"
	SessionFileJSON = "session.jsonl"

	MaxFiles       = 15000
	MaxDirectories = 4000
	MaxLineBytes   = 8 * 1024 * 1024
)

type Record struct {
	Line   int
	Kind   string
	TimeMS int64
	Data   map[string]interface{}
	Raw    json.RawMessage
}

type SessionRef struct {
	ID            string
	Path          string
	Dir           string
	DSHHome       string
	ModTimeUnixMS int64
	SizeBytes     int64
	Compressed    bool
	Meta          *SessionMeta
}

type SessionMeta struct {
	ID              string
	CWD             string
	ParentSessionID string
	Title           string
	Model           string
}

type Stats struct {
	Lines        int
	Decoded      int
	Malformed    int
	PartialTail  bool
	PartialFrame bool
	FirstError   error
}

type MapOptions struct {
	MinLine            int
	SkipSessionStarted bool
}

type MappedEvent struct {
	SourceLine int
	DedupID    string
	Event      schema.Event
}

type envelope struct {
	Type      string          `json:"type"`
	Time      interface{}     `json:"time"`
	CreatedAt interface{}     `json:"createdAt"`
	Data      json.RawMessage `json:"data"`
}

func decodeRecord(line []byte, lineNo int) (*Record, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	if strings.TrimSpace(env.Type) == "" {
		return nil, errors.New("dsh record type is empty")
	}
	data := map[string]interface{}{}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return nil, fmt.Errorf("decode dsh record data: %w", err)
		}
	}
	return &Record{
		Line:   lineNo,
		Kind:   env.Type,
		TimeMS: firstTimeMS(env.Time, env.CreatedAt),
		Data:   data,
		Raw:    append(json.RawMessage(nil), line...),
	}, nil
}

func firstTimeMS(values ...interface{}) int64 {
	for _, value := range values {
		if ms := timeMS(value); ms > 0 {
			return ms
		}
	}
	return 0
}

func timeMS(value interface{}) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return 0
		}
		if v < 1e12 {
			return int64(v * 1000)
		}
		return int64(v)
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return 0
		}
		if n, err := strconv.ParseFloat(text, 64); err == nil {
			return timeMS(n)
		}
		if t, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return t.UnixMilli()
		}
		return 0
	default:
		return 0
	}
}
