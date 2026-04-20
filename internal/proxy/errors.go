package proxy

import (
	"encoding/json"
	"strings"
)

// aError is the inner Anthropic error object.
type aError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// aErrorResponse is the Anthropic-format error envelope.
type aErrorResponse struct {
	Type  string `json:"type"` // always "error"
	Error aError `json:"error"`
}

// classifyError categorizes a transport error for logging and user-facing messages.
func classifyError(err error) string {
	if err == nil {
		return "INTERNAL"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "dial tcp"):
		return "CONNECTION"
	case strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout") ||
		strings.Contains(msg, "context canceled"):
		return "TIMEOUT"
	default:
		return "INTERNAL"
	}
}

// formatError creates an Anthropic-format error response body.
func formatError(errType, message string) []byte {
	resp := aErrorResponse{
		Type: "error",
		Error: aError{
			Type:    errType,
			Message: message,
		},
	}
	out, _ := json.Marshal(resp)
	return out
}

