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
	state       string
	kind        string
	reason      string
	replacement string
	force       bool
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

var memoryCandidatesCmd = &cobra.Command{
	Use:   "candidates",
	Short: "Review extracted memory candidates",
}

var memoryCandidatesListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List memory candidates",
	SilenceUsage: true,
	RunE:         runMemoryCandidatesList,
}

var memoryCandidatesShowCmd = &cobra.Command{
	Use:          "show <candidate-id>",
	Short:        "Show one memory candidate",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runMemoryCandidatesShow,
}

var memoryCandidatesApproveCmd = &cobra.Command{
	Use:          "approve <candidate-id>",
	Short:        "Approve a candidate into project memory",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runMemoryCandidatesApprove,
}

var memoryCandidatesRejectCmd = &cobra.Command{
	Use:          "reject <candidate-id>",
	Short:        "Reject a memory candidate",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runMemoryCandidatesReject,
}

var memoryCandidatesSupersedeCmd = &cobra.Command{
	Use:          "supersede <candidate-id>",
	Short:        "Mark a candidate as superseded by approved memory",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runMemoryCandidatesSupersede,
}

var memorySkillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Preview and install approved memory as Agent Skills",
}

var memorySkillsPreviewCmd = &cobra.Command{
	Use:          "preview <candidate-id>",
	Short:        "Preview the Agent Skill for an approved candidate",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runMemorySkillsPreview,
}

var memorySkillsInstallCmd = &cobra.Command{
	Use:          "install <candidate-id>",
	Short:        "Install the Agent Skill for an approved candidate",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runMemorySkillsInstall,
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
	Candidates       []asymptoteobserve.LearningCandidateV1  `json:"candidates,omitempty"`
}

func init() {
	rootCmd.AddCommand(memoryCmd)
	memoryCmd.AddCommand(memoryEvaluationsCmd)
	memoryCmd.AddCommand(memoryCandidatesCmd)
	memoryCmd.AddCommand(memorySkillsCmd)
	memoryEvaluationsCmd.AddCommand(memoryEvaluationsRunCmd)
	memoryEvaluationsCmd.AddCommand(memoryEvaluationsListCmd)
	memoryEvaluationsCmd.AddCommand(memoryEvaluationsShowCmd)
	memoryCandidatesCmd.AddCommand(memoryCandidatesListCmd)
	memoryCandidatesCmd.AddCommand(memoryCandidatesShowCmd)
	memoryCandidatesCmd.AddCommand(memoryCandidatesApproveCmd)
	memoryCandidatesCmd.AddCommand(memoryCandidatesRejectCmd)
	memoryCandidatesCmd.AddCommand(memoryCandidatesSupersedeCmd)
	memorySkillsCmd.AddCommand(memorySkillsPreviewCmd)
	memorySkillsCmd.AddCommand(memorySkillsInstallCmd)

	for _, c := range []*cobra.Command{memoryEvaluationsRunCmd, memoryEvaluationsListCmd, memoryEvaluationsShowCmd, memoryCandidatesListCmd, memoryCandidatesShowCmd, memoryCandidatesApproveCmd, memoryCandidatesRejectCmd, memoryCandidatesSupersedeCmd, memorySkillsPreviewCmd, memorySkillsInstallCmd} {
		c.Flags().BoolVar(&memoryOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&memoryOpts.systemMode, "system", false, "Use system endpoint paths")
		c.Flags().StringVar(&memoryOpts.logPath, "log-path", "", "Runtime JSONL log path")
		c.Flags().BoolVar(&memoryOpts.jsonOutput, "json", false, "Print machine-readable JSON")
		c.Flags().StringVar(&memoryOpts.projectPath, "project", "", "Project path for memory scoping (defaults to current directory)")
	}
	for _, c := range []*cobra.Command{memoryEvaluationsRunCmd, memoryEvaluationsListCmd, memoryCandidatesListCmd} {
		c.Flags().IntVar(&memoryOpts.limit, "limit", 25, "Limit returned traces or evaluations")
		c.Flags().IntVar(&memoryOpts.page, "page", 1, "Page number for collection output")
		c.Flags().StringVarP(&memoryOpts.query, "query", "q", "", "Free-text query")
	}
	memoryCandidatesListCmd.Flags().StringVar(&memoryOpts.state, "state", "", "Filter candidates by state")
	memoryCandidatesListCmd.Flags().StringVar(&memoryOpts.kind, "kind", "", "Filter candidates by memory kind")
	for _, c := range []*cobra.Command{memoryCandidatesApproveCmd, memoryCandidatesRejectCmd, memoryCandidatesSupersedeCmd} {
		c.Flags().StringVar(&memoryOpts.reason, "reason", "", "Review reason recorded with the candidate")
	}
	memoryCandidatesSupersedeCmd.Flags().StringVar(&memoryOpts.replacement, "replacement", "", "Approved memory ID that supersedes this candidate")
	memorySkillsInstallCmd.Flags().BoolVar(&memoryOpts.force, "force", false, "Overwrite an existing generated skill")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.harness, "harness", "", "Filter traces by harness")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.traceID, "trace", "", "Evaluate a single trace ID")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.since, "since", "", "RFC3339 lower time bound, inclusive")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.until, "until", "", "RFC3339 upper time bound, inclusive")
	memoryEvaluationsRunCmd.Flags().BoolVar(&memoryOpts.dryRun, "dry-run", false, "Preview selected traces, Jev calls, and estimated cost without writing evaluations")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.jevEndpoint, "jev-endpoint", "", "Jev System One endpoint (defaults to BEACON_JEV_ENDPOINT, then "+learning.DefaultJevEndpoint+")")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.jevAPIKey, "jev-api-key", "", "Jev API key (defaults to TYPESAFE_API_KEY, then BEACON_JEV_API_KEY)")
	memoryEvaluationsRunCmd.Flags().StringVar(&memoryOpts.jevModel, "jev-model", "", "Jev model name, such as jev-latest or a pinned Jev version (defaults to BEACON_JEV_MODEL, then "+learning.DefaultJevModel+")")
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
	evaluatorOpts := jevEvaluatorOptions()
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
		if candidate, ok := learning.CandidateFromEvaluation(eval); ok {
			if err := store.PutCandidate(candidate); err != nil {
				return err
			}
			result.Candidates = append(result.Candidates, candidate)
		}
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
	if len(result.Candidates) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Created %d candidate(s) for review.\n", len(result.Candidates))
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
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%.2f\t%.2f\t%s\n", question.ID, question.Probability, question.Confidence, learning.QuestionReason(question))
	}
	return nil
}

func runMemoryCandidatesList(cmd *cobra.Command, args []string) error {
	query, err := memoryCandidateQuery()
	if err != nil {
		return err
	}
	candidates, err := memoryStore().ListCandidates(query)
	if err != nil {
		return err
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(candidates)
	}
	for _, candidate := range candidates {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", candidate.ID, candidate.State, candidate.Kind, candidate.Title)
	}
	return nil
}

func runMemoryCandidatesShow(cmd *cobra.Command, args []string) error {
	candidate, ok, err := memoryStore().GetCandidate(args[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("candidate not found: %s", args[0])
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(candidate)
	}
	printCandidate(cmd, candidate)
	return nil
}

func runMemoryCandidatesApprove(cmd *cobra.Command, args []string) error {
	candidate, memory, err := learning.ApproveCandidate(memoryStore(), args[0], memoryOpts.reason)
	if err != nil {
		return err
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{"candidate": candidate, "memory": memory})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Approved %s as memory %s\n", candidate.ID, memory.ID)
	return nil
}

func runMemoryCandidatesReject(cmd *cobra.Command, args []string) error {
	candidate, err := learning.RejectCandidate(memoryStore(), args[0], memoryOpts.reason)
	if err != nil {
		return err
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(candidate)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Rejected %s\n", candidate.ID)
	return nil
}

func runMemoryCandidatesSupersede(cmd *cobra.Command, args []string) error {
	candidate, err := learning.SupersedeCandidate(memoryStore(), args[0], memoryOpts.replacement, memoryOpts.reason)
	if err != nil {
		return err
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(candidate)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Superseded %s with %s\n", candidate.ID, candidate.SupersededBy)
	return nil
}

func runMemorySkillsPreview(cmd *cobra.Command, args []string) error {
	candidate, memory, err := learning.CandidateMemoryForSkill(memoryStore(), args[0])
	if err != nil {
		return err
	}
	content := learning.RenderSkill(candidate, memory)
	result := learning.SkillInstallResult{Slug: learning.SkillSlug(memory), Content: content}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	fmt.Fprint(cmd.OutOrStdout(), content)
	return nil
}

func runMemorySkillsInstall(cmd *cobra.Command, args []string) error {
	candidate, memory, err := learning.CandidateMemoryForSkill(memoryStore(), args[0])
	if err != nil {
		return err
	}
	projectPath := memoryOpts.projectPath
	if projectPath == "" {
		projectPath = firstNonEmpty(memory.Project.Path, candidate.Project.Path)
	}
	result, err := learning.InstallSkill(projectPath, candidate, memory, memoryOpts.force)
	if err != nil {
		return err
	}
	if memoryOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Installed %s\n", result.Path)
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

func memoryCandidateQuery() (learning.Query, error) {
	query, err := memoryQuery()
	if err != nil {
		return learning.Query{}, err
	}
	query.State = memoryOpts.state
	query.Kind = memoryOpts.kind
	return query, nil
}

func printCandidate(cmd *cobra.Command, candidate asymptoteobserve.LearningCandidateV1) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", candidate.ID, candidate.State, candidate.Kind)
	fmt.Fprintln(cmd.OutOrStdout(), candidate.Title)
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), candidate.Body)
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

// jevEvaluatorOptions resolves each Jev setting from its flag, then the environment,
// then the built-in default. The flags must not carry the defaults themselves, or the
// environment variables documented for them would never be consulted.
func jevEvaluatorOptions() learning.EvaluatorOptions {
	return learning.EvaluatorOptions{
		Endpoint:     firstNonEmpty(memoryOpts.jevEndpoint, os.Getenv("BEACON_JEV_ENDPOINT"), learning.DefaultJevEndpoint),
		APIKey:       firstNonEmpty(memoryOpts.jevAPIKey, os.Getenv("TYPESAFE_API_KEY"), os.Getenv("BEACON_JEV_API_KEY")),
		Model:        firstNonEmpty(memoryOpts.jevModel, os.Getenv("BEACON_JEV_MODEL"), learning.DefaultJevModel),
		CostPerTrace: memoryOpts.jevCost,
		Timeout:      memoryOpts.timeout,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
