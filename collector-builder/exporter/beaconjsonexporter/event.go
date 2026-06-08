package beaconjsonexporter

import (
	"time"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
)

const (
	vendor        = beaconevent.Vendor
	product       = beaconevent.Product
	schemaVersion = beaconevent.SchemaVersion
)

type eventInfo = beaconevent.EventInfo
type endpointInfo = beaconevent.EndpointInfo
type userInfo = beaconevent.UserInfo
type harnessInfo = beaconevent.HarnessInfo
type sessionInfo = beaconevent.SessionInfo
type toolInfo = beaconevent.ToolInfo
type fileInfo = beaconevent.FileInfo
type commandInfo = beaconevent.CommandInfo
type mcpInfo = beaconevent.MCPInfo
type approvalInfo = beaconevent.ApprovalInfo
type promptInfo = beaconevent.PromptInfo
type beaconEvent = beaconevent.Event

func newBeaconEvent(action, category, severity, harnessName string, ts time.Time) beaconEvent {
	return beaconevent.NewEvent(action, category, severity, harnessName, ts)
}
