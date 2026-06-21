package logging

import (
	"encoding/json"
	"os"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// applyPromptRetention rewrites a prompt field in place according to the prompt
// retention mode configured in the trusted endpoint config file. It is a no-op in
// full mode (the default), so existing behavior is preserved unless an operator
// opts into redacted or metadata retention.
//
// The mode is read from the config file rather than an environment variable so a
// monitored runtime cannot relax its own redaction by setting an env var.
func applyPromptRetention(fields map[string]interface{}) {
	if fields == nil {
		return
	}
	promptVal, ok := fields["prompt"].(map[string]interface{})
	if !ok {
		return
	}
	text, _ := promptVal["text"].(string)
	info, content := asymptoteobserve.RetainPrompt(promptRetentionMode(), text)
	if info.Text == "" {
		delete(promptVal, "text")
	} else {
		promptVal["text"] = info.Text
	}
	if info.Hash != "" {
		promptVal["hash"] = info.Hash
	}
	if info.Length != 0 {
		promptVal["length"] = info.Length
	}
	if content != nil {
		marker := map[string]interface{}{
			"retention": content.Retention,
			"included":  content.Included,
		}
		if content.Redacted {
			marker["redacted"] = true
		}
		fields["content"] = marker
	}
}

// promptRetentionMode reads the prompt retention mode from the endpoint config file
// referenced by BEACON_ENDPOINT_CONFIG. Any missing/unreadable/invalid config falls
// back to full retention (the default), so telemetry never silently stops on a
// malformed config.
func promptRetentionMode() string {
	path := firstEnv("BEACON_ENDPOINT_CONFIG")
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		Redaction *struct {
			PromptMode string `json:"prompt_mode"`
		} `json:"redaction"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Redaction == nil {
		return ""
	}
	return cfg.Redaction.PromptMode
}
