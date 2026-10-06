package cmd

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// applyPromptID records which user prompt a hook event belongs to, as prompt.id.
//
// The runtime's own identifier is read from the payload's top level through the shared alias list,
// so Cursor's generation_id, Codex's and Muse Code's turn_id and Kimi Code's numbered turn_id all
// land in the one field the collector fills from Claude Code's and Gemini CLI's OTLP attributes.
// The envelope only, for the reason tool call ids are read from it only: tool arguments are
// model-authored, and an argument that happened to be named turn_id is not the runtime naming its
// turn.
//
// Every event a payload produces gets the id -- a tool call and the file edits it caused belong to
// the same prompt -- and an id a mapper already set is left alone.
func applyPromptID(fields, input map[string]interface{}) {
	if fields == nil || input == nil {
		return
	}
	if prompt, ok := fields["prompt"].(map[string]interface{}); ok {
		if id, _ := prompt["id"].(string); strings.TrimSpace(id) != "" {
			return
		}
	}
	id := asymptoteobserve.PromptIDFrom(input)
	if id == "" {
		return
	}
	fields["prompt"] = mergeNested(fields["prompt"], map[string]interface{}{"id": id})
}
