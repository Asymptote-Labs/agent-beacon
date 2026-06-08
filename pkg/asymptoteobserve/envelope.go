package asymptoteobserve

import "errors"

// Envelope is the raw, language-neutral trace input shape.
type Envelope struct {
	Vendor        string                 `json:"vendor"`
	SchemaVersion string                 `json:"schema_version"`
	Origin        Origin                 `json:"origin"`
	Harness       HarnessInfo            `json:"harness"`
	Session       *SessionInfo           `json:"session,omitempty"`
	Run           *RunInfo               `json:"run,omitempty"`
	Raw           map[string]interface{} `json:"raw,omitempty"`
}

func NewEnvelope(origin Origin, harness HarnessInfo, raw map[string]interface{}) Envelope {
	return Envelope{
		Vendor:        Vendor,
		SchemaVersion: SchemaVersion,
		Origin:        origin,
		Harness:       harness,
		Raw:           raw,
	}
}

func (e Envelope) Validate() error {
	if e.Vendor != Vendor {
		return errors.New("vendor must be beacon")
	}
	if e.SchemaVersion == "" {
		return errors.New("schema_version is required")
	}
	if e.Harness.Name == "" {
		return errors.New("harness.name is required")
	}
	if e.Origin != "" {
		switch e.Origin {
		case OriginLocal, OriginCloud, OriginCI:
		default:
			return errors.New("origin must be local, cloud, or ci")
		}
	}
	return nil
}

func (e Envelope) withDefaults() Envelope {
	if e.Vendor == "" {
		e.Vendor = Vendor
	}
	if e.SchemaVersion == "" {
		e.SchemaVersion = SchemaVersion
	}
	return e
}

func (e Envelope) copy() Envelope {
	out := e
	out.Session = cloneSession(e.Session)
	out.Run = cloneRun(e.Run)
	if e.Raw != nil {
		out.Raw = copyMap(e.Raw)
	}
	return out
}

func copyMap(input map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[key] = copyMap(typed)
		case []interface{}:
			out[key] = copySlice(typed)
		default:
			out[key] = typed
		}
	}
	return out
}

func copySlice(input []interface{}) []interface{} {
	out := make([]interface{}, len(input))
	for index, value := range input {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[index] = copyMap(typed)
		case []interface{}:
			out[index] = copySlice(typed)
		default:
			out[index] = typed
		}
	}
	return out
}
