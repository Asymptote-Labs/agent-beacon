package cursorusage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// Harness is the canonical harness name the events are recorded under: the same name Cursor's
// hooks and local session reader use, so a token report groups Admin API spend with the rest of
// Cursor's activity.
const Harness = "cursor"

// Source is recorded under raw.cursor.source so a reader can tell these events from anything a
// future local Cursor path might report.
const Source = "admin_api"

// HasUsage reports whether an event carries anything a token report can sum. The Admin API also
// lists requests billed per request rather than by token, which carry no token block and often no
// charge; an event with neither is a row of zeros and is not written.
func HasUsage(ev UsageEvent) bool {
	t := ev.TokenUsage
	if t.InputTokens > 0 || t.OutputTokens > 0 || t.CacheReadTokens > 0 || t.CacheWriteTokens > 0 {
		return true
	}
	return ev.ChargedCents != nil && *ev.ChargedCents > 0
}

// MapEvent converts one Admin API usage event into a Beacon token.usage event.
//
// The token counts go into gen_ai.usage exactly as Cursor reports them: inputTokens is the
// uncached input, cacheReadTokens and cacheWriteTokens are the cache buckets, so they map onto
// cache_read and cache_creation without double counting. cost_usd is chargedCents/100 -- what
// Cursor reports it charged, which is runtime-reported cost in the sense Beacon means. Cursor's
// token price (totalCents) is kept under raw.cursor rather than promoted, because a request
// included in a plan has a token price and no charge, and a report summing token prices would
// present a bill nobody received.
//
// No session is set. The Admin API reports requests per team member, not per conversation, and
// nothing in an event names the Composer it came from; attaching one by time overlap would be a
// guess presented as an observation. Fidelity is observed: Cursor names the request and its
// counts, Beacon derives nothing.
func MapEvent(ev UsageEvent) schema.Event {
	out := schema.NewEvent(schema.NewEventOptions{
		Action:   "token.usage",
		Category: "metric",
		Severity: schema.SeverityInfo,
		Fidelity: schema.FidelityObserved,
		Message:  "Cursor usage event",
		Origin:   schema.OriginLocal,
		Harness: schema.HarnessInfo{
			Name: Harness,
			// Poll: a pull of records Cursor committed after the fact. It cannot hold or deny
			// anything, which is what the marker exists to say.
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	out.Timestamp = schema.FormatTimestamp(ev.Timestamp)
	out.Model = ev.Model
	if user, ok := MemberUser(ev); ok {
		out.User = user
	}

	usage := &schema.GenAIUsageInfo{}
	if n := ev.TokenUsage.InputTokens; n > 0 {
		usage.InputTokens = &n
	}
	if n := ev.TokenUsage.OutputTokens; n > 0 {
		usage.OutputTokens = &n
	}
	if n := ev.TokenUsage.CacheReadTokens; n > 0 {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &n}
	}
	if n := ev.TokenUsage.CacheWriteTokens; n > 0 {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &n}
	}
	if ev.ChargedCents != nil {
		cost := *ev.ChargedCents / 100
		usage.CostUSD = &cost
	}
	out.GenAI = &schema.GenAIInfo{Usage: usage}

	raw := map[string]interface{}{
		"source":      Source,
		"kind":        ev.Kind,
		"max_mode":    ev.MaxMode,
		"token_based": ev.TokenBased,
		"headless":    ev.Headless,
	}
	if ev.TokenUsage.TotalCents != nil {
		raw["token_cents"] = *ev.TokenUsage.TotalCents
	}
	if ev.ChargedCents != nil {
		raw["charged_cents"] = *ev.ChargedCents
	}
	if ev.CursorTokenFeeCents != nil {
		raw["cursor_token_fee_cents"] = *ev.CursorTokenFeeCents
	}
	if ev.RequestsCosts != nil {
		raw["requests_costs"] = *ev.RequestsCosts
	}
	if ev.Chargeable != nil {
		raw["chargeable"] = *ev.Chargeable
	}
	if ev.UserID != "" {
		raw["user_id"] = ev.UserID
	}
	if ev.UserEmail != "" {
		raw["user_email"] = ev.UserEmail
	}
	out.Raw = map[string]interface{}{"cursor": raw}
	return out
}

// MemberUser is the Cursor team member who made the request, as the event's user.
//
// NewEvent fills the user with whoever ran the sync, which is right for an event a local agent
// produced and wrong here: a --team sync collects every member's requests, and crediting all of
// them to the admin who ran it would put the whole team's spend under one person in every by-user
// rollup. So the user is the member Cursor names. The UID is namespaced ("cursor:<id>") because it
// is a Cursor account id rather than an OS uid; the token rollups read a namespaced UID as an
// account identity and never reattribute it to the endpoint's local user.
//
// An event naming no member keeps the local user, since there is nobody else to credit.
func MemberUser(ev UsageEvent) (schema.UserInfo, bool) {
	id := strings.TrimSpace(ev.UserID)
	if id == "" {
		id = strings.TrimSpace(ev.UserEmail)
	}
	if id == "" {
		return schema.UserInfo{}, false
	}
	return schema.UserInfo{Name: strings.TrimSpace(ev.UserEmail), UID: "cursor:" + id}, true
}

// Fingerprint is an event's identity built from everything the Admin API says about it. The API
// gives events no id of their own, so this is the only stable name one has across two fetches of
// the same window.
func Fingerprint(ev UsageEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|%s|%t|%t|%d|%d|%d|%d|%s|%s|%s|%s|%s|%t|%s|%s",
		ev.Timestamp.UnixMilli(),
		ev.Model,
		ev.Kind,
		ev.MaxMode,
		ev.TokenBased,
		ev.TokenUsage.InputTokens,
		ev.TokenUsage.OutputTokens,
		ev.TokenUsage.CacheWriteTokens,
		ev.TokenUsage.CacheReadTokens,
		optionalAmount(ev.TokenUsage.TotalCents),
		optionalAmount(ev.ChargedCents),
		optionalAmount(ev.CursorTokenFeeCents),
		optionalAmount(ev.RequestsCosts),
		optionalBool(ev.Chargeable),
		ev.Headless,
		ev.UserID,
		ev.UserEmail,
	)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// DedupKeys names every event in a fetch. Two requests can be identical in every field the API
// reports -- same millisecond, model and counts -- and they are still two requests, so the nth
// copy of a fingerprint gets its own key rather than collapsing into the first. That is stable across
// fetches because identical events share a timestamp, so a window that includes one includes all
// of them.
func DedupKeys(events []UsageEvent) []string {
	seen := map[string]int{}
	keys := make([]string, len(events))
	for i, ev := range events {
		fp := Fingerprint(ev)
		n := seen[fp]
		seen[fp] = n + 1
		keys[i] = fp + "#" + strconv.Itoa(n)
	}
	return keys
}

func optionalAmount(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

func optionalBool(v *bool) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatBool(*v)
}
