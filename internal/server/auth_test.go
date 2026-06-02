package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lnsp/oxidize/internal/config"
)

// TestProtectedBearer covers the static-token bearer path used by the Oxide
// CLI/SDK: a matching "Authorization: Bearer <token>" authenticates, anything
// else does not, and bearer auth stays disabled when no token is configured.
func TestProtectedBearer(t *testing.T) {
	const token = "s3cret-token"

	cases := []struct {
		name      string
		apiToken  string
		header    string
		wantAuthd bool
	}{
		{"valid token", token, "Bearer " + token, true},
		{"wrong token", token, "Bearer nope", false},
		{"missing prefix", token, token, false},
		{"empty header", token, "", false},
		{"disabled when unset", "", "Bearer " + token, false},
		{"disabled empty header", "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: config.Config{APIToken: tc.apiToken}}
			var reached bool
			h := s.protected(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h(rec, req)

			if reached != tc.wantAuthd {
				t.Fatalf("authenticated=%v, want %v (status %d)", reached, tc.wantAuthd, rec.Code)
			}
			if !tc.wantAuthd && rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", rec.Code)
			}
		})
	}
}
