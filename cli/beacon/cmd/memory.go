package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

var memoryOpts struct {
	userMode    bool
	systemMode  bool
	logPath     string
	jsonOutput  bool
	projectPath string
	limit       int
	page        int
	query       string
	harness     string
	traceID     string
	since       string
	until       string
	dryRun      bool
	jevEndpoint string
	jevAPIKey   string
	jevModel    string
	jevCost     float64
	timeout     time.Duration
}

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Evaluate and review cross-harness agent memory",
}

var memoryEvaluationsCmd = &cobra.Command{
	Use:   "evaluations",
	Short: "Run and inspect local trace evaluations",
}

var memoryEvaluationsRunCmd = &cobra.Command{
	Use:          "run",
	Short:        "Evaluate local traces with Jev",
	SilenceUsage: true,
	RunE:         runMemoryEvaluationsRun,
}

var memoryEvaluationsListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List persisted trace evaluations",
	SilenceUsage: true,
	RunE:         runMemoryEvaluationsList,
}

var memoryEvaluationsShowCmd = &cobra.Command{
	Use:          "show <evaluation-id>",
	Short:        "Show one persisted trace evaluation",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runMemoryEvaluationsShow,
}

type evaluationRunResult struct {
	DryRun           bool                                    `json:"dry_run"`
	Project          asymptoteobserve.LearningProjectV1      `json:"project"`
	StorePath        string                                  `json:"store_path"`
	TraceCount       int                                     `json:"trace_count"`
	EstimatedCalls   int                                     `json:"estimated_calls"`
	EstimatedCostUSD float64                                 `json:"estimated_cost_usd"`
	Previews         []learning.EvaluationPreview            `json:"previews,omitempty"`
	Evaluations      []asymptoteobserve.LearningEvaluationV1 `json:"evaluations,omitempty"`
}

func init() {
	rootCmd.AddCommand(memoryCmd)
	memoryCmd.AddCommand(memoryEvaluationsCmd)
	memoryEvaluationsCmd.AddCommand(memoryEvaluationsRunCmd)
	memoryEvaluationsCmd.AddCommand(memoryEvaluationsListCmd)
	memoryEvaluationsCmd.AddCommand(memoryEvaluationsShowCmd)

	for _, c := range []*cobra.Command{memoryEvaluationsRunCmd, memoryEvaluationsListCmd, memoryEvaluationsShowCmd} {
		c.Flags().BoolVar(&memoryOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&memoryOpts.systemMode, "system", false, "Use system endpoint paths")
		c.Flags().StringVar(&memoryOpts.logPath, "log-path", "", "Runtime JSONL log path")
		c.Flags().BoolVar(&memoryOpts.jsonOutput, "json", false, "Print machine-readable JSON")
		c.Flags().StringVar(&memoryOpts.projectPath, "project", "", "Project path for memory scoping (defaults to current directory)")
	}
	for _, c := range []*cobra.Command{memoryEvaluationsRunCmd, memoryEvaluationsListCmd} {
		c.Flags().IntVar(&memoryOpts.limit, "limit", 25, "Limit returned traces or evaluations")
		c.Flags().IntVar(&memoryOpts.page, "page", 1, "Page number for collection output")
		c.Flags().StringVarP(&memoryOpts.query, "query", "q", "", "Free-text query")
	}
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.harness, "harness", "", "Filter traces by harness")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.traceID, "trace", "", "Evaluate a single trace ID")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.since, "since", "", "RFC3339 lower time bound, inclusive")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.until, "until", "", "RFC3339 upper time bound, inclusive")
	memoryEvaluationsRunCmd.Flags().BoolVar(&memoryOpts.dryRun, "dry-run", false, "Preview selected traces, Jev calls, and estimated cost without writing evaluations")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.jevEndpoint, "jev-endpoint", "", "Jev System One endpoint (default "+learning.DefaultJevEndpoint+")")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.jevAPIKey, "jev-api-key", "", "Jev API key (defaults to TYPESAFE_API_KEY, then BEACON_JEV_API_KEY)")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.jevModel, "jev-model", "", "Jev model name (default "+learning.DefaultJevModel+")")
	memoryEvaluationsRunCmd.Flags().Float64Var(&memoryOpts.jevCost, "jev-cost-per-trace", learning.DefaultCostPerTrace, "Estimated Jev cost per trace in USD")
	memoryEvaluationsRunCmd.Flags().DurationVar(&memoryOpts.timeout, "timeout", 10*time.Second, "Jev request timeout")
}

func runMemoryEvaluationsRun(cmd *cobra.Command, args []string) error {
	project, err := learning.ResolveProject(memoryOpts.projectPath)
	if err != nil {
		return err
	}
	store := memoryStore()
	inputs, err := selectedEvaluationInputs(project)
	if err != nil {
		return err
	}
	evaluatorOpts := learning.EvaluatorOptions{
		Endpoint:     firstNonEmpty(memoryOpts.jevEndpoint, os.Getenv("BEACON_JEV_ENDPOINT"), learning.DefaultJevEndpoint),
		APIKey:       firstNonEmpty(memoryOpts.jevAPIKey, os.Getenv("TYPESAFE_API_KEY"), os.Getenv("BEACON_JEV_API_KEY")),
		Model:        firstNonEmpty(memoryOpts.jevModel, os.Getenv("BEACON_JEV_MODEL"), learning.DefaultJevModel),
		CostPerTrace: memoryOpts.jevCost,
		Timeout:      memoryOpts.timeout,
	}
	result := evaluationRunResult{
		DryRun:           memoryOpts.dryRun,
		Project:          project,
		StorePath:        store.Path(),
		TraceCount:       len(inputs),
		EstimatedCalls:   len(inputs),
		EstimatedCostUSD: float64(len(inputs)) * memoryOpts.jevCost,
	}
	for _, input := range inputs {
		input.DryRun = memoryOpts.dryRun
		if memoryOpts.dryRun {
			result.Previews = append(result.Previews, learning.Preview(input, evaluatorOpts))
			continue
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		eval, err := learning.Evaluate(ctx, evaluatorOpts, input)
		if err != nil {
			return err
		}
		if err := store.PutEvaluation(eval); err != nil {
			return err
		}
		result.Evaluations = append(result.Evaluations, eval)
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	if result.DryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Selected %d trace(s); estimated Jev calls: %d; estimated cost: $%.5f\n", result.TraceCount, result.EstimatedCalls, result.EstimatedCostUSD)
		for _, preview := range result.Previews {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t$%.5f\n", preview.TraceID, preview.Title, preview.EstimatedCostUSD)
		}
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Evaluated %d trace(s); wrote results to %s\n", len(result.Evaluations), store.Path())
	for _, eval := range result.Evaluations {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%.2f\t%s\n", eval.ID, eval.Trace.ID, eval.Score, eval.Status)
	}
	return nil
}

func runMemoryEvaluationsList(cmd *cobra.Command, args []string) error {
	query, err := memoryQuery()
	if err != nil {
		return err
	}
	evals, err := memoryStore().ListEvaluations(query)
	if err != nil {
		return err
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(evals)
	}
	for _, eval := range evals {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%.2f\t%s\n", eval.ID, eval.Trace.ID, eval.Status, eval.Score, eval.Trace.Title)
	}
	return nil
}

func runMemoryEvaluationsShow(cmd *cobra.Command, args []string) error {
	eval, ok, err := memoryStore().GetEvaluation(args[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("evaluation not found: %s", args[0])
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(eval)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%.2f\n", eval.ID, eval.Trace.ID, eval.Status, eval.Score)
	for _, question := range eval.Questions {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%.2f\t%.2f\t%s\n", question.ID, question.Probability, question.Confidence, question.Reason)
	}
	return nil
}

func selectedEvaluationInputs(project asymptoteobserve.LearningProjectV1) ([]learning.EvaluationInput, error) {
	logPath := memoryLogPath()
	if memoryOpts.traceID != "" {
		show, ok, err := dashboard.ShowTrace(logPath, memoryOpts.traceID, dashboard.TraceQuery{Limit: 2000})
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("trace not found: %s", memoryOpts.traceID)
		}
		return []learning.EvaluationInput{{Project: project, Trace: show}}, nil
	}
	query := dashboard.TraceQuery{
		EventQuery: dashboard.EventQuery{
			Q:       memoryOpts.query,
			Harness: memoryOpts.harness,
		},
		Limit: memoryOpts.limit,
		Page:  memoryOpts.page,
	}
	var err error
	if query.Since, err = parseMemoryTime("since", memoryOpts.since); err != nil {
		return nil, err
	}
	if query.Until, err = parseMemoryTime("until", memoryOpts.until); err != nil {
		return nil, err
	}
	traces, err := dashboard.ReadTraceList(logPath, query)
	if err != nil {
		return nil, err
	}
	inputs := make([]learning.EvaluationInput, 0, len(traces.Traces))
	for _, summary := range traces.Traces {
		show, ok, err := dashboard.ShowTrace(logPath, summary.ID, dashboard.TraceQuery{Limit: 2000})
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		inputs = append(inputs, learning.EvaluationInput{Project: project, Trace: show})
	}
	return inputs, nil
}

func memoryQuery() (learning.Query, error) {
	project, err := learning.ResolveProject(memoryOpts.projectPath)
	if err != nil {
		return learning.Query{}, err
	}
	return learning.Query{
		ProjectID: project.ID,
		Q:         memoryOpts.query,
		Limit:     memoryOpts.limit,
		Page:      memoryOpts.page,
	}, nil
}

func memoryStore() *learning.Store {
	return learning.Open(learning.PathForRuntimeLog(memoryLogPath()))
}

func memoryLogPath() string {
	runtimeLog := lifecycle.ResolveRuntimeLog(memoryUserMode(), memoryOpts.logPath)
	return runtimeLog.EffectiveLogPath
}

func memoryUserMode() bool {
	return memoryOpts.userMode && !memoryOpts.systemMode
}

func parseMemoryTime(name, value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", name, err)
	}
	return parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
