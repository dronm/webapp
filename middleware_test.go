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

func TestGlobalCORSMiddlewareHandlesPreflightBeforeServeMuxMethodCheck(t *testing.T) {
	routes := []Route{
		POST(
			"/api/users/login",
			WithHandler(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("POST handler should not be called for OPTIONS preflight")
			}),
		),
	}

	handler := MustBuildRouter(
		&AppContext{},
		routes,
		CORSMiddleware(CORSConfig{
			AllowedOrigins:   []string{"http://localhost:3000"},
			AllowCredentials: true,
		}),
	)

	req := httptest.NewRequest(http.MethodOptions, "/api/users/login", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("allow origin = %q, want http://localhost:3000", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials = %q, want true", got)
	}
}
