package webapp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dronm/session"
)

func TestCORSMiddlewareHandlesPreflightBeforeSession(t *testing.T) {
	sessionCalled := false

	cors := CORSMiddleware(CORSConfig{
		AllowedOrigins:   []string{"https://example.com"},
		AllowCredentials: true,
	})
	sess := SessionMiddleware(func(w http.ResponseWriter, r *http.Request) (session.Session, error) {
		sessionCalled = true
		return nil, nil
	})

	handler := Chain(cors, sess)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called for OPTIONS preflight")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/test", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if sessionCalled {
		t.Fatalf("session resolver was called for preflight")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("allow origin = %q, want https://example.com", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials = %q, want true", got)
	}
}
