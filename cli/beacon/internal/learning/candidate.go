package learning

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// CandidateScoreThreshold is the minimum evaluation score (the mean of the
// rubric probabilities) a completed evaluation needs to become a candidate.
const CandidateScoreThreshold = 0.60

// TaskSuccessQuestionID names the rubric question that asks whether the trace
// completed the user's task.
const TaskSuccessQuestionID = "task_success"

// CandidateTaskSuccessThreshold is a precondition that the score cannot stand in
// for. The score is a mean, so two high answers about reusability and evidence
// could outvote a judge who said the task failed: 0.27/0.86/0.69 averages 0.6067
// and cleared CandidateScoreThreshold (#649). A trace whose task_success
// probability is below this value is never promoted, whatever its score.
const CandidateTaskSuccessThreshold = 0.50

// PromotionDecision reports whether a completed evaluation should become a
// memory candidate, and when it should not, why. It applies the task_success
// precondition before the score threshold, so the stored Score keeps its meaning
// (the plain mean shown to reviewers) while a failed task can no longer be
// averaged into a candidate. An evaluation without a task_success answer fails
// the precondition, because nothing says the task succeeded.
func PromotionDecision(eval asymptoteobserve.LearningEvaluationV1) (bool, string) {
	if eval.Status != asymptoteobserve.LearningEvaluationStatusCompleted {
		return false, fmt.Sprintf("evaluation status is %s, not %s", eval.Status, asymptoteobserve.LearningEvaluationStatusCompleted)
	}
	taskSuccess, ok := questionProbability(eval, TaskSuccessQuestionID)
	if !ok {
		return false, TaskSuccessQuestionID + " was not answered"
	}
	if taskSuccess < CandidateTaskSuccessThreshold {
		return false, fmt.Sprintf("%s %s is below %.2f", TaskSuccessQuestionID, formatBelow(taskSuccess, CandidateTaskSuccessThreshold), CandidateTaskSuccessThreshold)
	}
	if eval.Score < CandidateScoreThreshold {
		return false, fmt.Sprintf("score %s is below %.2f", formatBelow(eval.Score, CandidateScoreThreshold), CandidateScoreThreshold)
	}
	return true, ""
}

// formatBelow renders value, known to be below limit, with the fewest decimals
// (at least two) that still read as below it, so a reason never says "0.50 is
// below 0.50" for 0.4999.
func formatBelow(value, limit float64) string {
	for precision := 2; precision <= 6; precision++ {
		text := strconv.FormatFloat(value, 'f', precision, 64)
		if parsed, err := strconv.ParseFloat(text, 64); err == nil && parsed < limit {
			return text
		}
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func questionProbability(eval asymptoteobserve.LearningEvaluationV1, id string) (float64, bool) {
	for _, question := range eval.Questions {
		if question.ID == id {
			return question.Probability, true
		}
	}
	return 0, false
}

func CandidateFromEvaluation(eval asymptoteobserve.LearningEvaluationV1) (asymptoteobserve.LearningCandidateV1, bool) {
	if ok, _ := PromotionDecision(eval); !ok {
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

// ApprovalEdits carries reviewer-authored text that replaces the candidate's
// fields in the approved memory. The evaluator scores traces but does not write
// lessons, so a reviewer (or an agent the reviewer is working with) supplies the
// title, body, and applicability after reading the source trace. Empty fields
// keep the candidate's value. The candidate record itself is left as the
// evaluator produced it, so the review history shows what was changed.
type ApprovalEdits struct {
	Title         string
	Body          string
	Applicability string
	Kind          string
	Tags          []string
}

// ValidMemoryKind reports whether kind is one of the memory kinds Beacon defines.
func ValidMemoryKind(kind string) bool {
	switch kind {
	case asymptoteobserve.LearningMemoryKindWorkflow,
		asymptoteobserve.LearningMemoryKindCorrection,
		asymptoteobserve.LearningMemoryKindDebuggingPattern,
		asymptoteobserve.LearningMemoryKindGotcha,
		asymptoteobserve.LearningMemoryKindConvention:
		return true
	}
	return false
}

func ApproveCandidate(store *Store, id, reason string) (asymptoteobserve.LearningCandidateV1, asymptoteobserve.LearningMemoryV1, error) {
	return ApproveCandidateWithEdits(store, id, reason, ApprovalEdits{})
}

func ApproveCandidateWithEdits(store *Store, id, reason string, edits ApprovalEdits) (asymptoteobserve.LearningCandidateV1, asymptoteobserve.LearningMemoryV1, error) {
	kind := strings.TrimSpace(edits.Kind)
	if kind != "" && !ValidMemoryKind(kind) {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, fmt.Errorf("unknown memory kind %q (want workflow, correction, debugging_pattern, gotcha, or convention)", kind)
	}
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
		Kind:          firstNonEmpty(kind, candidate.Kind),
		Title:         firstNonEmpty(edits.Title, candidate.Title),
		Body:          firstNonEmpty(edits.Body, candidate.Body),
		Applicability: firstNonEmpty(edits.Applicability, candidate.Applicability),
		Tags:          approvedTags(edits.Tags, candidate.Tags),
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

// approvedTags uses the reviewer's tags when any were given, trimmed and without
// empties or duplicates, and otherwise the candidate's.
func approvedTags(edited, original []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tag := range edited {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	if len(out) > 0 {
		return out
	}
	return append([]string(nil), original...)
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

// candidateBody uses per-question rationale only when a compatible evaluator
// supplied it through the legacy questions/results response shapes. TypeSafe Noul
// answers contain probabilities, not rationale, so the normal Jev path explicitly
// says that no lesson text was extracted instead of presenting scores as guidance.
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
