package codec

import (
	"encoding/json"
	"net/http"
)

// anthropicErrorType is Anthropic's error.type for an HTTP status.
func anthropicErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	}
	if status >= 500 {
		return "api_error"
	}
	return "invalid_request_error"
}

func anthropicErrorBody(status int, message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": anthropicErrorType(status), "message": message},
	})
	return b
}

// openAIErrorBody is the error shape Chat Completions and Responses share.
func openAIErrorBody(status int, message string) []byte {
	typ := "invalid_request_error"
	switch {
	case status == http.StatusUnauthorized:
		typ = "authentication_error"
	case status == http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case status >= 500:
		typ = "server_error"
	}
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": typ, "param": nil, "code": nil},
	})
	return b
}
