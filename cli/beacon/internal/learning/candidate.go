package learning

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const CandidateScoreThreshold = 0.60

func CandidateFromEvaluation(eval asymptoteobserve.LearningEvaluationV1) (asymptoteobserve.LearningCandidateV1, bool) {
	if eval.Status != asymptoteobserve.LearningEvaluationStatusCompleted {
		return asymptoteobserve.LearningCandidateV1{}, false
	}
	if eval.Score < CandidateScoreThreshold {
		return asymptoteobserve.LearningCandidateV1{}, false
	}
	kind := candidateKind(eval)
	title := candidateTitle(eval, kind)
	body := candidateBody(eval)
	candidate := asymptoteobserve.LearningCandidateV1{
		SchemaVersion:      asymptoteobserve.LearningSchemaVersion,
		State:              asymptoteobserve.LearningCandidateStateCandidate,
		Kind:               kind,
		Title:              title,
		Body:               body,
		Applicability:      candidateApplicability(eval),
		Tags:               candidateTags(eval, kind),
		Project:            eval.Project,
		SourceEvaluationID: eval.ID,
		Evidence: []asymptoteobserve.LearningEvidenceV1{
			{
				TraceID:  eval.Trace.ID,
				EventIDs: append([]string(nil), eval.Trace.EventIDs...),
				Summary:  strings.TrimSpace(eval.Trace.Title),
			},
		},
	}
	candidate.ID = CandidateID(candidate)
	return candidate, true
}

func ApproveCandidate(store *Store, id, reason string) (asymptoteobserve.LearningCandidateV1, asymptoteobserve.LearningMemoryV1, error) {
	candidate, ok, err := store.GetCandidate(id)
	if err != nil {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, err
	}
	if !ok {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, fmt.Errorf("candidate not found: %s", id)
	}
	if candidate.State != asymptoteobserve.LearningCandidateStateCandidate {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, fmt.Errorf("candidate %s is %s, not candidate", id, candidate.State)
	}
	now := nowString()
	memory := asymptoteobserve.LearningMemoryV1{
		SchemaVersion: asymptoteobserve.LearningSchemaVersion,
		CandidateID:   candidate.ID,
		Kind:          candidate.Kind,
		Title:         candidate.Title,
		Body:          candidate.Body,
		Applicability: candidate.Applicability,
		Tags:          append([]string(nil), candidate.Tags...),
		Project:       candidate.Project,
		Evidence:      append([]asymptoteobserve.LearningEvidenceV1(nil), candidate.Evidence...),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	memory.ID = MemoryID(memory)
	candidate.State = asymptoteobserve.LearningCandidateStateApproved
	candidate.MemoryID = memory.ID
	candidate.ApprovedAt = now
	candidate.ReviewReason = strings.TrimSpace(reason)
	if err := store.PutMemory(memory); err != nil {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, err
	}
	if err := store.PutCandidate(candidate); err != nil {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, err
	}
	return candidate, memory, nil
}

func RejectCandidate(store *Store, id, reason string) (asymptoteobserve.LearningCandidateV1, error) {
	candidate, ok, err := store.GetCandidate(id)
	if err != nil {
		return asymptoteobserve.LearningCandidateV1{}, err
	}
	if !ok {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("candidate not found: %s", id)
	}
	if candidate.State != asymptoteobserve.LearningCandidateStateCandidate {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("candidate %s is %s, not candidate", id, candidate.State)
	}
	candidate.State = asymptoteobserve.LearningCandidateStateRejected
	candidate.RejectedAt = nowString()
	candidate.ReviewReason = strings.TrimSpace(reason)
	if err := store.PutCandidate(candidate); err != nil {
		return asymptoteobserve.LearningCandidateV1{}, err
	}
	return candidate, nil
}

func SupersedeCandidate(store *Store, id, replacementMemoryID, reason string) (asymptoteobserve.LearningCandidateV1, error) {
	if strings.TrimSpace(replacementMemoryID) == "" {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("replacement memory id is required")
	}
	replacement, ok, err := store.GetMemory(replacementMemoryID)
	if err != nil {
		return asymptoteobserve.LearningCandidateV1{}, err
	}
	if !ok {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("replacement memory not found: %s", replacementMemoryID)
	}
	candidate, ok, err := store.GetCandidate(id)
	if err != nil {
		return asymptoteobserve.LearningCandidateV1{}, err
	}
	if !ok {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("candidate not found: %s", id)
	}
	if candidate.Project.ID != replacement.Project.ID {
		return asymptoteobserve.LearningCandidateV1{}, fmt.Errorf("replacement memory belongs to a different project")
	}
	now := nowString()
	candidate.State = asymptoteobserve.LearningCandidateStateSuperseded
	candidate.SupersededAt = now
	candidate.SupersededBy = replacement.ID
	candidate.ReviewReason = strings.TrimSpace(reason)
	if err := store.PutCandidate(candidate); err != nil {
		return asymptoteobserve.LearningCandidateV1{}, err
	}
	if candidate.MemoryID != "" {
		memory, ok, err := store.GetMemory(candidate.MemoryID)
		if err != nil {
			return asymptoteobserve.LearningCandidateV1{}, err
		}
		if ok {
			memory.SupersededBy = replacement.ID
			memory.UpdatedAt = now
			if err := store.PutMemory(memory); err != nil {
				return asymptoteobserve.LearningCandidateV1{}, err
			}
		}
	}
	return candidate, nil
}

func candidateKind(eval asymptoteobserve.LearningEvaluationV1) string {
	title := strings.ToLower(eval.Trace.Title)
	switch {
	case strings.Contains(title, "convention") || strings.Contains(title, "standard"):
		return asymptoteobserve.LearningMemoryKindConvention
	case strings.Contains(title, "gotcha") || strings.Contains(title, "pitfall"):
		return asymptoteobserve.LearningMemoryKindGotcha
	case strings.Contains(title, "workflow") || strings.Contains(title, "process"):
		return asymptoteobserve.LearningMemoryKindWorkflow
	case strings.Contains(title, "fix") || strings.Contains(title, "debug") || strings.Contains(title, "fail"):
		return asymptoteobserve.LearningMemoryKindDebuggingPattern
	default:
		return asymptoteobserve.LearningMemoryKindCorrection
	}
}

func candidateTitle(eval asymptoteobserve.LearningEvaluationV1, kind string) string {
	title := strings.TrimSpace(eval.Trace.Title)
	if title == "" {
		title = eval.Trace.ID
	}
	return strings.TrimSpace(strings.ReplaceAll(kind, "_", " ")) + ": " + title
}

// candidateBody builds the candidate text from the evaluator's per-question
// rationale. Without any rationale there is no lesson to extract, so the body says
// so instead of presenting bare scores as guidance (#620).
func candidateBody(eval asymptoteobserve.LearningEvaluationV1) string {
	var rationale []string
	for _, question := range eval.Questions {
		if reason := QuestionReason(question); reason != "" {
			rationale = append(rationale, fmt.Sprintf("- %s: %s", question.ID, reason))
		}
	}
	var lines []string
	if len(rationale) > 0 {
		lines = append(lines, "Reusable lesson extracted from a reviewed Beacon trace.")
		lines = append(lines, "")
		lines = append(lines, "Evaluator rationale:")
		lines = append(lines, rationale...)
	} else {
		lines = append(lines, "Beacon trace flagged for review by evaluation scores.")
		lines = append(lines, "")
		lines = append(lines, "The evaluator returned scores with no rationale, so no lesson text was extracted. Review the source trace before approving.")
	}
	lines = append(lines, "")
	lines = append(lines, "Trace: "+eval.Trace.ID)
	if eval.Trace.Harness.Name != "" {
		lines = append(lines, "Harness: "+eval.Trace.Harness.Name)
	}
	if eval.Trace.Title != "" {
		lines = append(lines, "Observed workflow: "+eval.Trace.Title)
	}
	lines = append(lines, "")
	lines = append(lines, "Evaluation signals:")
	for _, question := range eval.Questions {
		lines = append(lines, fmt.Sprintf("- %s: %.2f", question.ID, question.Probability))
	}
	return strings.Join(lines, "\n")
}

func candidateApplicability(eval asymptoteobserve.LearningEvaluationV1) string {
	if eval.Trace.Harness.Name == "" {
		return "When a future agent works on a similar task in this project."
	}
	return "When a future agent in this project hits a similar workflow, regardless of harness."
}

func candidateTags(eval asymptoteobserve.LearningEvaluationV1, kind string) []string {
	tags := []string{"beacon", kind}
	if eval.Trace.Harness.Name != "" {
		tags = append(tags, eval.Trace.Harness.Name)
	}
	return tags
}
