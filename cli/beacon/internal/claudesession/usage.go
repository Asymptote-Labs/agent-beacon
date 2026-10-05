package claudesession

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// Token usage is counted per API response, not per transcript record.
//
// Claude Code writes one streamed response as one line per content block -- a thinking line, a
// text line, a tool_use line -- each its own record with its own uuid, all sharing the response's
// message.id and requestId and each carrying a copy of message.usage. A line written before the
// stream's final message_delta carries the message_start snapshot, whose output_tokens is a
// placeholder, and the tool a tool_use line names can run and write its result before the next
// block line of the same response arrives. Counting usage per record therefore counted each
// response once per block, and could count a placeholder as if it were a response of its own.
//
// So every usage-bearing record is grouped by the response it belongs to: (message.id, requestId),
// message.id alone when there is no requestId, and the record itself when there is neither. Each
// response gets one token.usage event carrying its largest-output snapshot (on a tie, the later
// record's), placed on the first record that reports that count, under an event id derived from
// the response rather than from the record.
//
// Sweeps are incremental, and one can land between two block lines of a response. The mapper
// always sees the whole file (MinLine only gates emission), so the part of a response at or
// before the cursor is what earlier sweeps counted: the largest snapshot among those records.
// That holds because the usage event sits on the first record reporting the count it carries,
// and because a sweep that stops partway never leaves the cursor between a response's first
// uncounted record and its unwritten usage event (MappedEvent.RetryFrom). A later sweep that
// finds a larger snapshot emits only the difference, under an id derived from the response and
// the record carrying it; one that finds nothing larger emits nothing. Rerunning a sweep, or
// sweeping the finished file from scratch, cannot count a response twice.
//
// A forked or resumed session can replay earlier records, same message.id and requestId, into
// another file. Within one sweep a responseLedger carries what every file read so far has counted,
// so a replayed response is counted only where it was first seen. Across sweeps the ledger is
// not kept -- it would grow with every response ever made -- so a replay into a file first read
// in a later sweep than its original is counted again there, under the same response-derived
// event id the original carries.

// responseLedger is what one sweep has counted for each response, keyed by responseKey.
type responseLedger map[string]*schema.GenAIUsageInfo

// usagePlan is one token.usage event to emit on a record line.
type usagePlan struct {
	usage      *schema.GenAIUsageInfo
	coordinate string
	retryFrom  int
}

type responseUsage struct {
	key        string
	coordinate string
	counted    *schema.GenAIUsageInfo
	best       *schema.GenAIUsageInfo
	bestRecord Record
	lines      []lineOutput
}

type lineOutput struct {
	line   int
	output int64
}

// responseKey names the API response a record belongs to, or "" when the record does not say.
func responseKey(entry Entry) string {
	id := strings.TrimSpace(stringValue(entry.Message["id"]))
	if id == "" {
		return ""
	}
	if request := strings.TrimSpace(entry.RequestID); request != "" {
		return "response:" + id + "\x00" + request
	}
	return "response:" + id
}

// planUsage decides, before any record is mapped, which record lines carry a token.usage event and
// what each one counts. ledger may be nil.
func planUsage(ref SessionRef, records []Record, minLine int, ledger responseLedger) map[int]usagePlan {
	var order []*responseUsage
	byKey := map[string]*responseUsage{}
	for _, record := range records {
		if record.Entry.Type != "assistant" {
			continue
		}
		usage := usageFromMessage(record.Entry.Message)
		if usage == nil {
			continue
		}
		key := responseKey(record.Entry)
		group := byKey[key]
		if key == "" || group == nil {
			group = &responseUsage{key: key, coordinate: key}
			if key == "" {
				// The event id every record-keyed usage event has always had.
				group.coordinate = recordCoordinate(ref, record)
			} else {
				byKey[key] = group
			}
			order = append(order, group)
		}
		output := outputTokens(usage)
		if record.Line <= minLine {
			if group.counted == nil || output >= outputTokens(group.counted) {
				group.counted = usage
			}
			continue
		}
		group.lines = append(group.lines, lineOutput{line: record.Line, output: output})
		switch {
		case group.best == nil || output > outputTokens(group.best):
			group.best = usage
			group.bestRecord = record
		case output == outputTokens(group.best):
			// Same count, later record: take its snapshot, keep the event where the count
			// first appeared.
			group.best = usage
		}
	}

	plans := map[int]usagePlan{}
	for _, group := range order {
		prior := group.counted
		if group.key != "" && ledger != nil {
			if seen := ledger[group.key]; seen != nil && (prior == nil || outputTokens(seen) > outputTokens(prior)) {
				prior = seen
			}
		}
		if group.best != nil {
			plan := usagePlan{retryFrom: group.lines[0].line}
			switch {
			case prior == nil:
				plan.usage = group.best
				plan.coordinate = group.coordinate + ":usage"
			case outputTokens(group.best) > outputTokens(prior):
				plan.usage = usageDelta(group.best, prior)
				plan.coordinate = group.coordinate + ":usage:" + recordCoordinate(ref, group.bestRecord)
				for _, lo := range group.lines {
					if lo.output > outputTokens(prior) {
						plan.retryFrom = lo.line
						break
					}
				}
			}
			if plan.usage != nil {
				plans[group.bestRecord.Line] = plan
			}
		}
		if group.key != "" && ledger != nil {
			for _, usage := range []*schema.GenAIUsageInfo{prior, group.best} {
				if usage != nil && (ledger[group.key] == nil || outputTokens(usage) > outputTokens(ledger[group.key])) {
					ledger[group.key] = usage
				}
			}
		}
	}
	return plans
}

func recordCoordinate(ref SessionRef, record Record) string {
	return firstNonEmpty(record.Entry.UUID, ref.Path+":"+itoa(record.Line))
}

// outputTokens orders snapshots of one response. A snapshot without an output count sorts below
// every snapshot that has one.
func outputTokens(usage *schema.GenAIUsageInfo) int64 {
	if usage == nil || usage.OutputTokens == nil {
		return -1
	}
	return *usage.OutputTokens
}

// usageDelta is what best adds to an already-counted snapshot of the same response, field by
// field. Only output grows over a stream, but every field is differenced so nothing is counted
// twice whichever does.
func usageDelta(best, prior *schema.GenAIUsageInfo) *schema.GenAIUsageInfo {
	sub := func(next, counted *int64) *int64 {
		if next == nil {
			return nil
		}
		v := *next
		if counted != nil {
			v -= *counted
		}
		if v < 0 {
			v = 0
		}
		return &v
	}
	out := &schema.GenAIUsageInfo{
		InputTokens:  sub(best.InputTokens, prior.InputTokens),
		OutputTokens: sub(best.OutputTokens, prior.OutputTokens),
	}
	// Minus differences the one-hour subset with the writes it belongs to, so a correction for a
	// grown snapshot carries the one-hour writes it adds and never more than its own writes.
	out.CacheCreation = best.CacheCreation.Minus(prior.CacheCreation)
	if best.CacheRead != nil {
		var counted *int64
		if prior.CacheRead != nil {
			counted = prior.CacheRead.InputTokens
		}
		out.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: sub(best.CacheRead.InputTokens, counted)}
	}
	if best.Reasoning != nil {
		var counted *int64
		if prior.Reasoning != nil {
			counted = prior.Reasoning.OutputTokens
		}
		out.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: sub(best.Reasoning.OutputTokens, counted)}
	}
	return out
}
