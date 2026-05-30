package oxide

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// ErrorBody is the Oxide error envelope returned for all 4xx/5xx responses.
type ErrorBody struct {
	Message   string  `json:"message"`
	RequestID string  `json:"request_id"`
	ErrorCode *string `json:"error_code,omitempty"`
}

// WriteJSON writes v as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError writes an Oxide-shaped error body. A random request id is
// generated so the console's error display has something to show.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, ErrorBody{Message: message, RequestID: randomID()})
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
