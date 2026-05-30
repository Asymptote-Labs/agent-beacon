package schema

import (
	"github.com/asymptote-labs/agent-beacon/pkg/asymptotetrace"
)

const (
	Vendor        = asymptotetrace.Vendor
	Product       = asymptotetrace.Product
	SchemaVersion = asymptotetrace.SchemaVersion
)

type Severity = asymptotetrace.Severity

const (
	SeverityInfo     = asymptotetrace.SeverityInfo
	SeverityLow      = asymptotetrace.SeverityLow
	SeverityMedium   = asymptotetrace.SeverityMedium
	SeverityHigh     = asymptotetrace.SeverityHigh
	SeverityCritical = asymptotetrace.SeverityCritical
)

type Origin = asymptotetrace.Origin

const (
	OriginLocal = asymptotetrace.OriginLocal
	OriginCloud = asymptotetrace.OriginCloud
	OriginCI    = asymptotetrace.OriginCI
)

type EventInfo = asymptotetrace.EventInfo
type EndpointInfo = asymptotetrace.EndpointInfo
type UserInfo = asymptotetrace.UserInfo
type HarnessInfo = asymptotetrace.HarnessInfo
type SessionInfo = asymptotetrace.SessionInfo
type RunInfo = asymptotetrace.RunInfo
type ToolInfo = asymptotetrace.ToolInfo
type FileInfo = asymptotetrace.FileInfo
type CommandInfo = asymptotetrace.CommandInfo
type MCPInfo = asymptotetrace.MCPInfo
type ApprovalInfo = asymptotetrace.ApprovalInfo
type PolicyInfo = asymptotetrace.PolicyInfo
type PromptInfo = asymptotetrace.PromptInfo
type ContentInfo = asymptotetrace.ContentInfo
type DestinationInfo = asymptotetrace.DestinationInfo
type HealthInfo = asymptotetrace.HealthInfo
type Event = asymptotetrace.Event
type NewEventOptions = asymptotetrace.NewEventOptions
type Envelope = asymptotetrace.Envelope

const (
	ContentRetentionMetadata = asymptotetrace.ContentRetentionMetadata
	ContentRetentionRedacted = asymptotetrace.ContentRetentionRedacted
	ContentRetentionFull     = asymptotetrace.ContentRetentionFull
)

var NewEvent = asymptotetrace.NewEvent
