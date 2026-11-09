package anthropic

import (
	"encoding/json"
	"net/http"
)

// ErrType is the Anthropic error type for an HTTP status.
func ErrType(code int) string {
	switch {
	case code == http.StatusTooManyRequests:
		return "rate_limit_error"
	case code == http.StatusForbidden:
		return "permission_error"
	case code == http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case code >= 500:
		return "api_error"
	}
	return "invalid_request_error"
}

// ErrorBody is an Anthropic error response body.
func ErrorBody(typ, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
	return b
}

// WriteError writes an Anthropic-style error with the type that fits code.
func WriteError(w http.ResponseWriter, code int, msg string) {
	WriteErrorType(w, code, ErrType(code), msg)
}

// WriteErrorType writes an Anthropic-style error with an explicit type.
func WriteErrorType(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(ErrorBody(typ, msg))
}
