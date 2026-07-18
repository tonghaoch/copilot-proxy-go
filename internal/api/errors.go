package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// HTTPError wraps an HTTP response error.
type HTTPError struct {
	Message    string
	Type       string
	StatusCode int
	Body       string
	Cause      error
}

func (e *HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s — %s", e.StatusCode, e.Message, e.Body)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

func (e *HTTPError) Unwrap() error { return e.Cause }

// InvalidRequest creates a client-facing 400 error for malformed or invalid
// request data while retaining the underlying cause for logs and inspection.
func InvalidRequest(message string, cause error) *HTTPError {
	return &HTTPError{
		Message:    message,
		Type:       "invalid_request_error",
		StatusCode: http.StatusBadRequest,
		Cause:      cause,
	}
}

// NewHTTPError creates an HTTPError from an HTTP response.
func NewHTTPError(resp *http.Response) *HTTPError {
	body, _ := io.ReadAll(resp.Body)
	return &HTTPError{
		Message:    resp.Status,
		StatusCode: resp.StatusCode,
		Body:       string(body),
	}
}

// ErrorResponse is the JSON error format returned to clients.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// ForwardError writes a structured JSON error response.
func ForwardError(w http.ResponseWriter, err error) {
	statusCode := http.StatusInternalServerError
	message := err.Error()
	errType := "internal_error"

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		statusCode = httpErr.StatusCode
		message = httpErr.Message
		if httpErr.Type != "" {
			errType = httpErr.Type
		} else if statusCode >= 400 && statusCode < 500 {
			errType = "invalid_request_error"
		}
		// Try to parse the body as JSON error
		var parsed struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(httpErr.Body), &parsed) == nil {
			if parsed.Error.Message != "" {
				message = parsed.Error.Message
				if parsed.Error.Type != "" {
					errType = parsed.Error.Type
				}
			} else if parsed.Message != "" {
				message = parsed.Message
			}
		}
	}

	slog.Error("request error", "status", statusCode, "message", message)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{
			Message: message,
			Type:    errType,
		},
	})
}
