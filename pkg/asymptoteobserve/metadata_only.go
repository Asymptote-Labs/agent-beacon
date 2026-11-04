package asymptoteobserve

import "reflect"

// StripToMetadata removes from ev every field Beacon Cloud's Metadata-only forwarding removes: the
// prompt's text, the message, raw, command output, the file diff, gen_ai input, output and system
// instructions, a tool's description, definitions, call arguments and result, retrieval queries and
// documents, an evaluation's explanation, an agent's description, stop sequences, and approval and
// policy reasons. What stays is the metadata that forwarding keeps: identifiers, names, paths, the
// command line, decisions, model and token usage. A content marker the event carries is rewritten to
// say so (retention metadata, not included, redacted).
//
// It mirrors the beacon_runtime_metadata Vector transform (metadata-transforms.toml in the
// forwarding pack), including which emptied blocks are dropped, so anything else that publishes an
// endpoint's events in Metadata-only mode, such as `beacon share`, sends no more than forwarding
// would. A test in the forwarding pack holds the two to the same list of fields.
//
// Only ev changes: every block it edits is copied first, so another event that shares one of ev's
// pointers keeps its content.
func StripToMetadata(ev *Event) {
	if ev == nil {
		return
	}
	ev.Message = ""
	ev.Raw = nil
	if ev.Prompt != nil {
		// prompt.id names the turn an event belongs to and stays.
		prompt := *ev.Prompt
		prompt.Text = ""
		ev.Prompt = nonZeroOrNil(prompt)
	}
	if ev.Command != nil {
		command := *ev.Command
		command.Output = ""
		ev.Command = &command
	}
	if ev.File != nil {
		file := *ev.File
		file.Diff = ""
		ev.File = &file
	}
	if ev.Approval != nil {
		approval := *ev.Approval
		approval.Reason = ""
		ev.Approval = nonZeroOrNil(approval)
	}
	if ev.Policy != nil {
		policy := *ev.Policy
		policy.Reason = ""
		ev.Policy = nonZeroOrNil(policy)
	}
	if ev.GenAI != nil {
		ev.GenAI = stripGenAIToMetadata(*ev.GenAI)
	}
	if ev.Content != nil {
		content := *ev.Content
		content.Retention = ContentRetentionMetadata
		content.Included = false
		content.Redacted = true
		ev.Content = &content
	}
}

func stripGenAIToMetadata(genAI GenAIInfo) *GenAIInfo {
	genAI.Input = nil
	genAI.Output = nil
	genAI.SystemInstructions = nil
	genAI.SystemInstructionsText = ""
	genAI.Retrieval = nil
	if genAI.Tool != nil {
		tool := *genAI.Tool
		tool.Description = ""
		tool.Definitions = nil
		if tool.Call != nil {
			call := *tool.Call
			call.Arguments = nil
			call.Result = nil
			call.ResultText = ""
			tool.Call = nonZeroOrNil(call)
		}
		genAI.Tool = nonZeroOrNil(tool)
	}
	if genAI.Evaluation != nil {
		evaluation := *genAI.Evaluation
		evaluation.Explanation = ""
		genAI.Evaluation = nonZeroOrNil(evaluation)
	}
	if genAI.Agent != nil {
		agent := *genAI.Agent
		agent.Description = ""
		genAI.Agent = nonZeroOrNil(agent)
	}
	if genAI.Request != nil {
		request := *genAI.Request
		request.StopSequences = nil
		genAI.Request = nonZeroOrNil(request)
	}
	return nonZeroOrNil(genAI)
}

// nonZeroOrNil is a pointer to value, or nil when value is its type's zero value, as the transform
// drops an object it emptied.
func nonZeroOrNil[T any](value T) *T {
	if reflect.ValueOf(value).IsZero() {
		return nil
	}
	return &value
}
