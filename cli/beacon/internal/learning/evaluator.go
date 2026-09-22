package learning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	RubricVersion       = "beacon.learning.rubric.v1"
	DefaultJevEndpoint  = "https://api.typesafe.ai/v1/systemone"
	DefaultJevModel     = "jev-latest"
	DefaultCostPerTrace = 0.00035
	maxProjectionEvents = 80
	maxProjectionText   = 1200
)

var RubricQuestions = []asymptoteobserve.LearningEvaluationQuestionV1{
	{ID: "task_success", Prompt: "Did the trace complete the user's engineering task successfully?"},
	{ID: "reusable_correction", Prompt: "Does the trace contain a correction or debugging pattern that future agents should reuse?"},
	{ID: "evidence_supported", Prompt: "Is the reusable lesson supported by concrete events in the trace?"},
}

type EvaluatorOptions struct {
	Endpoint     string
	APIKey       string
	Model        string
	HTTPClient   *http.Client
	CostPerTrace float64
	Timeout      time.Duration
}

type EvaluationInput struct {
	Project asymptoteobserve.LearningProjectV1
	Trace   asymptoteobserve.TraceShowResultV1
	DryRun  bool
}

type EvaluationPreview struct {
	TraceID          string  `json:"trace_id"`
	Title            string  `json:"title,omitempty"`
	EventCount       int     `json:"event_count"`
	EstimatedCalls   int     `json:"estimated_calls"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	Reason           string  `json:"reason,omitempty"`
}

type Projection struct {
	TraceID    string              `json:"trace_id"`
	Title      string              `json:"title,omitempty"`
	Harness    string              `json:"harness,omitempty"`
	Repository string              `json:"repository,omitempty"`
	Events     []ProjectedEvent    `json:"events"`
	Questions  []ProjectedQuestion `json:"-"`
}

type ProjectedEvent struct {
	Number  int    `json:"number"`
	Type    string `json:"type"`
	Action  string `json:"action"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Content string `json:"content,omitempty"`
}

type ProjectedQuestion struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

type jevRequest struct {
	Model     string                 `json:"model,omitempty"`
	State     map[string]interface{} `json:"state"`
	Questions map[string]jevQuestion `json:"questions"`
}

type jevResponse struct {
	Model   string                                      `json:"model,omitempty"`
	Answers map[string]jevAnswer                        `json:"answers"`
	Usage   *asymptoteobserve.LearningEvaluationUsageV1 `json:"usage,omitempty"`
	// Compatibility with tests or local shims that still return the earlier internal shape.
	Questions       []asymptoteobserve.LearningEvaluationQuestionV1 `json:"questions,omitempty"`
	Results         []asymptoteobserve.LearningEvaluationQuestionV1 `json:"results,omitempty"`
	Score           *float64                                        `json:"score,omitempty"`
	CostEstimateUSD *float64                                        `json:"cost_estimate_usd,omitempty"`
}

type jevQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type jevAnswer struct {
	Type          string             `json:"type,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Probability   *float64           `json:"probability,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

func Evaluate(ctx context.Context, opts EvaluatorOptions, input EvaluationInput) (asymptoteobserve.LearningEvaluationV1, error) {
	projection := BuildProjection(input.Trace)
	evaluation := baseEvaluation(opts, input, projection)
	if input.DryRun {
		evaluation.Status = asymptoteobserve.LearningEvaluationStatusDryRun
		evaluation.CostEstimateUSD = costPerTrace(opts)
		evaluation.ID = EvaluationID(evaluation)
		return evaluation, nil
	}
	if strings.TrimSpace(opts.Endpoint) == "" {
		opts.Endpoint = DefaultJevEndpoint
	}
	resp, err := callEvaluator(ctx, opts, projection)
	if err != nil {
		evaluation.Status = asymptoteobserve.LearningEvaluationStatusFailed
		evaluation.Error = err.Error()
		evaluation.ID = EvaluationID(evaluation)
		return evaluation, err
	}
	questions := questionsFromJevAnswers(resp.Answers)
	if len(questions) == 0 {
		questions = resp.Questions
	}
	if len(questions) == 0 {
		questions = resp.Results
	}
	if len(questions) == 0 {
		return asymptoteobserve.LearningEvaluationV1{}, fmt.Errorf("Jev response did not include answers, questions, or results")
	}
	evaluation.Questions = normalizeQuestionResults(questions)
	evaluation.Score = evaluationScore(evaluation.Questions)
	if resp.Score != nil {
		evaluation.Score = *resp.Score
	}
	if resp.Model != "" {
		evaluation.EvaluatorModel = resp.Model
	}
	if resp.Usage != nil {
		evaluation.Usage = resp.Usage
		if resp.Usage.CostUSD > 0 {
			evaluation.CostEstimateUSD = resp.Usage.CostUSD
		}
	}
	if resp.CostEstimateUSD != nil {
		evaluation.CostEstimateUSD = *resp.CostEstimateUSD
	} else if evaluation.CostEstimateUSD == 0 {
		evaluation.CostEstimateUSD = costPerTrace(opts)
	}
	evaluation.Status = asymptoteobserve.LearningEvaluationStatusCompleted
	evaluation.ID = EvaluationID(evaluation)
	return evaluation, nil
}

func Preview(input EvaluationInput, opts EvaluatorOptions) EvaluationPreview {
	trace := input.Trace.Trace
	return EvaluationPreview{
		TraceID:          trace.ID,
		Title:            trace.Title,
		EventCount:       input.Trace.Range.TotalEvents,
		EstimatedCalls:   1,
		EstimatedCostUSD: costPerTrace(opts),
		Reason:           "explicit evaluation command; hooks and collectors do not call Jev",
	}
}

func BuildProjection(show asymptoteobserve.TraceShowResultV1) Projection {
	projection := Projection{
		TraceID: show.Trace.ID,
		Title:   show.Trace.Title,
		Harness: show.Trace.Harness.Name,
	}
	if show.Trace.Repository != nil {
		projection.Repository = firstNonEmpty(show.Trace.Repository.RemoteURL, show.Trace.Repository.Path)
	}
	for _, question := range RubricQuestions {
		projection.Questions = append(projection.Questions, ProjectedQuestion{ID: question.ID, Prompt: question.Prompt})
	}
	events := show.Events
	if len(events) > maxProjectionEvents {
		// Long sessions resolve at the end: keep the opening context and the tail
		// instead of only the first maxProjectionEvents events, otherwise the
		// evaluator never sees the fix or outcome a reusable lesson depends on.
		head := maxProjectionEvents / 2
		tail := maxProjectionEvents - head
		combined := make([]asymptoteobserve.TraceEventV1, 0, maxProjectionEvents+1)
		combined = append(combined, events[:head]...)
		combined = append(combined, asymptoteobserve.TraceEventV1{
			Type:    "omitted",
			Summary: fmt.Sprintf("%d events omitted from the middle of this trace", len(events)-maxProjectionEvents),
		})
		combined = append(combined, events[len(events)-tail:]...)
		events = combined
	}
	for _, event := range events {
		projected := ProjectedEvent{
			Number:  event.Number,
			Type:    event.Type,
			Action:  event.Action,
			Title:   cleanText(event.Title),
			Summary: cleanText(event.Summary),
		}
		if event.Content != nil {
			projected.Content = cleanText(event.Content.Text)
		}
		if event.Command != nil && projected.Content == "" {
			projected.Content = cleanText(event.Command.Command)
		}
		projection.Events = append(projection.Events, projected)
	}
	return projection
}

func RubricHash() string {
	data, _ := json.Marshal(RubricQuestions)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func baseEvaluation(opts EvaluatorOptions, input EvaluationInput, projection Projection) asymptoteobserve.LearningEvaluationV1 {
	trace := input.Trace.Trace
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ref := asymptoteobserve.LearningTraceRefV1{
		ID:      trace.ID,
		Title:   trace.Title,
		Harness: trace.Harness,
		Session: trace.Session,
		Repo:    trace.Repository,
	}
	for _, event := range input.Trace.Events {
		ref.EventIDs = append(ref.EventIDs, event.ID)
	}
	return asymptoteobserve.LearningEvaluationV1{
		SchemaVersion:   asymptoteobserve.LearningSchemaVersion,
		Status:          asymptoteobserve.LearningEvaluationStatusCompleted,
		CreatedAt:       now,
		UpdatedAt:       now,
		Project:         input.Project,
		RubricVersion:   RubricVersion,
		RubricHash:      RubricHash(),
		Evaluator:       evaluatorName(opts),
		EvaluatorModel:  modelName(opts),
		DryRun:          input.DryRun,
		Trace:           ref,
		CostEstimateUSD: costPerTrace(opts),
		Questions:       projectionQuestionDefaults(projection),
	}
}

func callEvaluator(ctx context.Context, opts EvaluatorOptions, projection Projection) (jevResponse, error) {
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(jevRequest{
		Model: modelName(opts),
		State: map[string]interface{}{
			"trace":          projection,
			"rubric_version": RubricVersion,
			"rubric_hash":    RubricHash(),
		},
		Questions: jevQuestions(),
	})
	if err != nil {
		return jevResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.Endpoint, bytes.NewReader(body))
	if err != nil {
		return jevResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+opts.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return jevResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return jevResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return jevResponse{}, fmt.Errorf("Jev endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var parsed jevResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return jevResponse{}, err
	}
	return parsed, nil
}

func normalizeQuestionResults(results []asymptoteobserve.LearningEvaluationQuestionV1) []asymptoteobserve.LearningEvaluationQuestionV1 {
	byID := map[string]asymptoteobserve.LearningEvaluationQuestionV1{}
	for _, result := range results {
		byID[result.ID] = result
	}
	out := make([]asymptoteobserve.LearningEvaluationQuestionV1, 0, len(RubricQuestions))
	for _, question := range RubricQuestions {
		result := byID[question.ID]
		if result.ID == "" {
			result.ID = question.ID
		}
		if result.Prompt == "" {
			result.Prompt = question.Prompt
		}
		if result.Probability < 0 {
			result.Probability = 0
		}
		if result.Probability > 1 {
			result.Probability = 1
		}
		out = append(out, result)
	}
	return out
}

func jevQuestions() map[string]jevQuestion {
	out := make(map[string]jevQuestion, len(RubricQuestions))
	for _, question := range RubricQuestions {
		out[question.ID] = jevQuestion{
			Type:         "noul",
			Instructions: question.Prompt,
			Criteria: map[string]string{
				"true":  "The trace satisfies this criterion.",
				"false": "The trace does not satisfy this criterion.",
			},
		}
	}
	return out
}

func questionsFromJevAnswers(answers map[string]jevAnswer) []asymptoteobserve.LearningEvaluationQuestionV1 {
	if len(answers) == 0 {
		return nil
	}
	out := make([]asymptoteobserve.LearningEvaluationQuestionV1, 0, len(RubricQuestions))
	for _, question := range RubricQuestions {
		answer, ok := answers[question.ID]
		if !ok {
			continue
		}
		probability := 0.0
		switch {
		case answer.Noul != nil:
			probability = *answer.Noul
		case answer.Probability != nil:
			probability = *answer.Probability
		case answer.Score != nil:
			probability = *answer.Score
		}
		out = append(out, asymptoteobserve.LearningEvaluationQuestionV1{
			ID:          question.ID,
			Prompt:      question.Prompt,
			Probability: probability,
			Confidence:  answer.Confidence,
			Reason:      answer.Type,
		})
	}
	return out
}

func projectionQuestionDefaults(projection Projection) []asymptoteobserve.LearningEvaluationQuestionV1 {
	out := make([]asymptoteobserve.LearningEvaluationQuestionV1, 0, len(projection.Questions))
	for _, question := range projection.Questions {
		out = append(out, asymptoteobserve.LearningEvaluationQuestionV1{ID: question.ID, Prompt: question.Prompt})
	}
	return out
}

func evaluationScore(results []asymptoteobserve.LearningEvaluationQuestionV1) float64 {
	if len(results) == 0 {
		return 0
	}
	var sum float64
	for _, result := range results {
		sum += result.Probability
	}
	return sum / float64(len(results))
}

func evaluatorName(opts EvaluatorOptions) string {
	if opts.Endpoint == "" {
		return "jev:" + DefaultJevEndpoint
	}
	return "jev:" + opts.Endpoint
}

func modelName(opts EvaluatorOptions) string {
	if strings.TrimSpace(opts.Model) != "" {
		return strings.TrimSpace(opts.Model)
	}
	return DefaultJevModel
}

func costPerTrace(opts EvaluatorOptions) float64 {
	if opts.CostPerTrace > 0 {
		return opts.CostPerTrace
	}
	return DefaultCostPerTrace
}

func cleanText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = asymptoteobserve.CleanString(value, maxProjectionText, true)
	return value
}
