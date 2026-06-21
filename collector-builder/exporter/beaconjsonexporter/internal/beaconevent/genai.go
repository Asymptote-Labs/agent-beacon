package beaconevent

import "strings"

func setUsageInput(u *GenAIUsageInfo, v int64)  { u.InputTokens = &v }
func setUsageOutput(u *GenAIUsageInfo, v int64) { u.OutputTokens = &v }
func setUsageCacheRead(u *GenAIUsageInfo, v int64) {
	u.CacheRead = &GenAIUsageCacheReadInfo{InputTokens: &v}
}
func setUsageCacheCreation(u *GenAIUsageInfo, v int64) {
	u.CacheCreation = &GenAIUsageCacheCreationInfo{InputTokens: &v}
}
func setUsageReasoning(u *GenAIUsageInfo, v int64) {
	u.Reasoning = &GenAIUsageReasoningInfo{OutputTokens: &v}
}

func ApplyTokenUsage(event *Event, tokenType string, value int64) {
	if event.GenAI == nil {
		event.GenAI = &GenAIInfo{}
	}
	if event.GenAI.Usage == nil {
		event.GenAI.Usage = &GenAIUsageInfo{}
	}
	usage := event.GenAI.Usage
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(tokenType)), "_", "") {
	case "input", "prompt":
		setUsageInput(usage, value)
	case "output", "completion":
		setUsageOutput(usage, value)
	case "cacheread", "cachedinput":
		setUsageCacheRead(usage, value)
	case "cachecreation":
		setUsageCacheCreation(usage, value)
	case "reasoning", "reasoningoutput":
		setUsageReasoning(usage, value)
	default:
		if event.GenAI.Token == nil && tokenType != "" {
			event.GenAI.Token = &GenAITokenInfo{Type: tokenType}
		}
	}
	if tokenType != "" && event.GenAI.Token == nil {
		event.GenAI.Token = &GenAITokenInfo{Type: tokenType}
	}
}

func GenAIFromAttrs(attrs map[string]interface{}) *GenAIInfo {
	genai := &GenAIInfo{}
	if description := FirstString(attrs, "gen_ai.agent.description"); description != "" || FirstString(attrs, "gen_ai.agent.id", "gen_ai.agent.name", "gen_ai.agent.version") != "" {
		genai.Agent = &GenAIAgentInfo{
			Description: description,
			ID:          FirstString(attrs, "gen_ai.agent.id"),
			Name:        FirstString(attrs, "gen_ai.agent.name"),
			Version:     FirstString(attrs, "gen_ai.agent.version"),
		}
	}
	if id := FirstString(attrs, "gen_ai.conversation.id"); id != "" {
		genai.Conversation = &GenAIConversationInfo{ID: id}
	}
	if id := FirstString(attrs, "gen_ai.data_source.id"); id != "" {
		genai.DataSource = &GenAIDataSourceInfo{ID: id}
	}
	if count, ok := IntAttr(attrs, "gen_ai.embeddings.dimension.count"); ok {
		genai.Embeddings = &GenAIEmbeddingsInfo{DimensionCount: &count}
	}
	if explanation := FirstString(attrs, "gen_ai.evaluation.explanation"); explanation != "" || FirstString(attrs, "gen_ai.evaluation.name", "gen_ai.evaluation.score.label") != "" || HasAttr(attrs, "gen_ai.evaluation.score.value") {
		genai.Evaluation = &GenAIEvaluationInfo{
			Explanation: explanation,
			Name:        FirstString(attrs, "gen_ai.evaluation.name"),
		}
		if label := FirstString(attrs, "gen_ai.evaluation.score.label"); label != "" || HasAttr(attrs, "gen_ai.evaluation.score.value") {
			genai.Evaluation.Score = &GenAIEvaluationScoreInfo{Label: label}
			if value, ok := FloatAttr(attrs, "gen_ai.evaluation.score.value"); ok {
				genai.Evaluation.Score.Value = &value
			}
		}
	}
	if messages, ok := AnyAttr(attrs, "gen_ai.input.messages"); ok {
		genai.Input = &GenAIInputInfo{Messages: messages}
	} else if messages := LegacyMessages(attrs, "gen_ai.prompt.", "user"); len(messages) > 0 {
		genai.Input = &GenAIInputInfo{Messages: messages}
	} else if messages, ok := AnyAttr(attrs, "llm.prompts", "gen_ai.prompts"); ok {
		genai.Input = &GenAIInputInfo{Messages: messages}
	}
	if name := FirstString(attrs, "gen_ai.operation.name"); name != "" {
		genai.Operation = &GenAIOperationInfo{Name: name}
	}
	if messages, ok := AnyAttr(attrs, "gen_ai.output.messages"); ok {
		genai.Output = &GenAIOutputInfo{Messages: messages, Type: FirstString(attrs, "gen_ai.output.type")}
	} else if messages := LegacyMessages(attrs, "gen_ai.completion.", "assistant"); len(messages) > 0 {
		genai.Output = &GenAIOutputInfo{Messages: messages, Type: FirstString(attrs, "gen_ai.output.type")}
	} else if messages, ok := AnyAttr(attrs, "llm.completions", "gen_ai.completions"); ok {
		genai.Output = &GenAIOutputInfo{Messages: messages, Type: FirstString(attrs, "gen_ai.output.type")}
	} else if outputType := FirstString(attrs, "gen_ai.output.type"); outputType != "" {
		genai.Output = &GenAIOutputInfo{Type: outputType}
	}
	if name := FirstString(attrs, "gen_ai.prompt.name"); name != "" {
		genai.Prompt = &GenAIPromptInfo{Name: name}
	}
	if name := FirstString(attrs, "gen_ai.provider.name", "gen_ai.system"); name != "" {
		genai.Provider = &GenAIProviderInfo{Name: name}
	}
	if request := GenAIRequestFromAttrs(attrs); request != nil {
		genai.Request = request
	}
	if response := GenAIResponseFromAttrs(attrs); response != nil {
		genai.Response = response
	}
	if documents, ok := AnyAttr(attrs, "gen_ai.retrieval.documents"); ok {
		genai.Retrieval = &GenAIRetrievalInfo{Documents: documents, QueryText: FirstString(attrs, "gen_ai.retrieval.query.text")}
	} else if query := FirstString(attrs, "gen_ai.retrieval.query.text"); query != "" {
		genai.Retrieval = &GenAIRetrievalInfo{QueryText: query}
	}
	if instructions, ok := AnyAttr(attrs, "gen_ai.system_instructions"); ok {
		genai.SystemInstructions = instructions
	}
	if tokenType := FirstString(attrs, "gen_ai.token.type"); tokenType != "" {
		genai.Token = &GenAITokenInfo{Type: tokenType}
	}
	if tool := GenAIToolFromAttrs(attrs); tool != nil {
		genai.Tool = tool
	}
	if usage := GenAIUsageFromAttrs(attrs); usage != nil {
		genai.Usage = usage
	}
	if name := FirstString(attrs, "gen_ai.workflow.name"); name != "" {
		genai.Workflow = &GenAIWorkflowInfo{Name: name}
	}
	if IsZeroJSON(genai) {
		return nil
	}
	return genai
}

func GenAIRequestFromAttrs(attrs map[string]interface{}) *GenAIRequestInfo {
	request := &GenAIRequestInfo{
		Model:           FirstString(attrs, "gen_ai.request.model", "llm.request.model"),
		EncodingFormats: StringSliceAttr(attrs, "gen_ai.request.encoding_formats"),
		StopSequences:   StringSliceAttr(attrs, "gen_ai.request.stop_sequences"),
	}
	if value, ok := IntAttr(attrs, "gen_ai.request.choice.count"); ok {
		request.ChoiceCount = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.frequency_penalty"); ok {
		request.FrequencyPenalty = &value
	}
	if value, ok := IntAttr(attrs, "gen_ai.request.max_tokens", "llm.request.max_tokens"); ok {
		request.MaxTokens = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.presence_penalty"); ok {
		request.PresencePenalty = &value
	}
	if value, ok := IntAttr(attrs, "gen_ai.request.seed"); ok {
		request.Seed = &value
	}
	if value, ok := BoolAttr(attrs, "gen_ai.request.stream"); ok {
		request.Stream = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.temperature", "llm.request.temperature"); ok {
		request.Temperature = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.top_k"); ok {
		request.TopK = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.top_p"); ok {
		request.TopP = &value
	}
	if IsZeroJSON(request) {
		return nil
	}
	return request
}

func GenAIResponseFromAttrs(attrs map[string]interface{}) *GenAIResponseInfo {
	response := &GenAIResponseInfo{
		FinishReasons: StringSliceAttr(attrs, "gen_ai.response.finish_reasons"),
		ID:            FirstString(attrs, "gen_ai.response.id"),
		Model:         FirstString(attrs, "gen_ai.response.model", "llm.response.model"),
	}
	if value, ok := FloatAttr(attrs, "gen_ai.response.time_to_first_chunk"); ok {
		response.TimeToFirstChunk = &value
	}
	if IsZeroJSON(response) {
		return nil
	}
	return response
}

func GenAIToolFromAttrs(attrs map[string]interface{}) *GenAIToolInfo {
	tool := &GenAIToolInfo{
		Description: FirstString(attrs, "gen_ai.tool.description"),
		Name:        FirstString(attrs, genAIToolNameKeys...),
		Type:        FirstString(attrs, "gen_ai.tool.type"),
	}
	if definitions, ok := AnyAttr(attrs, "gen_ai.tool.definitions"); ok {
		tool.Definitions = definitions
	}
	if args, ok := AnyAttr(attrs, "gen_ai.tool.call.arguments", "function_args", "arguments"); ok {
		tool.Call = &GenAIToolCallInfo{Arguments: args, ID: FirstString(attrs, "gen_ai.tool.call.id")}
	} else if id := FirstString(attrs, "gen_ai.tool.call.id"); id != "" {
		tool.Call = &GenAIToolCallInfo{ID: id}
	}
	if result, ok := AnyAttr(attrs, "gen_ai.tool.call.result"); ok {
		if tool.Call == nil {
			tool.Call = &GenAIToolCallInfo{}
		}
		tool.Call.Result = result
	}
	if IsZeroJSON(tool) {
		return nil
	}
	return tool
}

// GenAIUsageFromAttrs normalizes runtime token usage into the canonical
// gen_ai.usage struct. Alongside the OTel GenAI and legacy llm.usage.* semconv
// names it accepts Claude Code's bare claude_code.llm_request span attribute
// names (input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens)
// so span-level usage and the per-step session drilldown carry real counts.
func GenAIUsageFromAttrs(attrs map[string]interface{}) *GenAIUsageInfo {
	usage := &GenAIUsageInfo{}
	if value, ok := Int64Attr(attrs, "gen_ai.usage.cache_creation.input_tokens", "gen_ai.usage.cache_creation_input_tokens", "cache_creation_tokens"); ok {
		setUsageCacheCreation(usage, value)
	}
	if value, ok := Int64Attr(attrs, "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read_input_tokens", "cache_read_tokens"); ok {
		setUsageCacheRead(usage, value)
	}
	if value, ok := Int64Attr(attrs, "gen_ai.usage.input_tokens", "llm.usage.prompt_tokens", "gen_ai.usage.prompt_tokens", "input_tokens"); ok {
		setUsageInput(usage, value)
	}
	if value, ok := Int64Attr(attrs, "gen_ai.usage.output_tokens", "llm.usage.completion_tokens", "gen_ai.usage.completion_tokens", "output_tokens"); ok {
		setUsageOutput(usage, value)
	}
	if value, ok := Int64Attr(attrs, "gen_ai.usage.reasoning.output_tokens"); ok {
		setUsageReasoning(usage, value)
	}
	if value, ok := FloatAttr(attrs, "gen_ai.usage.cost"); ok {
		usage.CostUSD = &value
	}
	if IsZeroJSON(usage) {
		return nil
	}
	return usage
}
