package learning

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// CandidateFromTrace builds a review candidate from a trace and a lesson written by
// whoever read it. No evaluator is involved: the reviewer states the lesson and the
// candidate records the trace and its event IDs as evidence. SourceEvaluationID stays
// empty, which is how an authored candidate is told apart from a Jev-scored one.
//
// Unlike ApproveCandidateWithEdits, where every field is optional because the
// candidate supplies defaults, here the kind, title and body are required: there is
// no evaluator text to fall back on.
func CandidateFromTrace(project asymptoteobserve.LearningProjectV1, trace asymptoteobserve.TraceShowResultV1, lesson ApprovalEdits) (asymptoteobserve.LearningCandidateV1, error) {
	if strings.TrimSpace(trace.Trace.ID) == "" {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("trace id is required")
	}
	kind := strings.TrimSpace(lesson.Kind)
	title := strings.TrimSpace(lesson.Title)
	body := strings.TrimRight(lesson.Body, " \t\r\n")
	if kind == "" {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("--kind is required (want workflow, correction, debugging_pattern, gotcha, or convention)")
	}
	if !ValidMemoryKind(kind) {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("unknown memory kind %q (want workflow, correction, debugging_pattern, gotcha, or convention)", kind)
	}
	if title == "" {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("--title is required")
	}
	if strings.TrimSpace(body) == "" {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("a body is required; pass --body or --body-file")
	}
	applicability := strings.TrimSpace(lesson.Applicability)
	if applicability == "" {
		applicability = "When a future agent in this project hits a similar workflow, regardless of harness."
	}
	defaultTags := []string{"beacon", kind}
	if trace.Trace.Harness.Name != "" {
		defaultTags = append(defaultTags, trace.Trace.Harness.Name)
	}
	evidence := asymptoteobserve.LearningEvidenceV1{
		TraceID: strings.TrimSpace(trace.Trace.ID),
		Summary: strings.TrimSpace(trace.Trace.Title),
	}
	for _, event := range trace.Events {
		evidence.EventIDs = append(evidence.EventIDs, event.ID)
	}
	candidate := asymptoteobserve.LearningCandidateV1{
		SchemaVersion: asymptoteobserve.LearningSchemaVersion,
		State:         asymptoteobserve.LearningCandidateStateCandidate,
		Kind:          kind,
		Title:         title,
		Body:          body,
		Applicability: applicability,
		Tags:          approvedTags(lesson.Tags, defaultTags),
		Project:       project,
		Evidence:      []asymptoteobserve.LearningEvidenceV1{evidence},
	}
	candidate.ID = CandidateID(candidate)
	return candidate, nil
}
