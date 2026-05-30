package asymptotetrace

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
	event := Event{
		Vendor:        e.Vendor,
		Product:       Product,
		SchemaVersion: e.SchemaVersion,
		Event:         EventInfo{Kind: "agent_runtime", Action: "envelope.received"},
		Severity:      SeverityInfo,
		Endpoint:      EndpointInfo{OS: "unknown"},
		Harness:       e.Harness,
		Origin:        e.Origin,
	}
	return event.Validate()
}
