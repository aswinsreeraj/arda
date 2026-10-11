package http

import (
	"encoding/json"
	"net/http"
)

// Response matches the documented ARDA error format:
// {"error":{"code":"...","message":"...","details":{}}}
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody contains the machine-readable code, message, and details object.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// WriteErrorResponse writes a standardized ARDA error response without leaking internal secrets.
func WriteErrorResponse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
			Details: map[string]any{},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// WriteJSON writes a successful JSON response.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
