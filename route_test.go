package webapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGroupAddsPrefixAndMiddlewareInOrder(t *testing.T) {
	routes := []Route{}
	calls := []string{}

	first := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "first-before")
			next.ServeHTTP(w, r)
			calls = append(calls, "first-after")
		})
	}
	second := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "second-before")
			next.ServeHTTP(w, r)
			calls = append(calls, "second-after")
		})
	}

	group := NewGroup("/api", &routes, first)
	group.GET(
		"/ping",
		WithHandler(func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "handler")
			_ = WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
		}),
		WithMiddleware(second),
	)

	if len(routes) != 1 {
		t.Fatalf("route count = %d, want 1", len(routes))
	}
	if routes[0].Pattern != "/api/ping" {
		t.Fatalf("pattern = %q, want /api/ping", routes[0].Pattern)
	}
	if len(routes[0].Middleware) != 2 {
		t.Fatalf("middleware count = %d, want 2", len(routes[0].Middleware))
	}

	handler := MustBuildRouter(&AppContext{}, routes)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	handler.ServeHTTP(rec, req)

	want := []string{"first-before", "second-before", "handler", "second-after", "first-after"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	}
}

func TestValidateRoutesReportsMissingServiceConfiguration(t *testing.T) {
	err := ValidateRoutes([]Route{
		GET("/supplier/list", WithName("supplier list")),
	})
	if err == nil {
		t.Fatal("ValidateRoutes() error = nil, want error")
	}
}

func TestQueryIDMiddlewareGeneratesIDWhenHeaderIsAbsent(t *testing.T) {
	handler := QueryIDMiddleware("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryID := QueryIDFromContext(r.Context())
		if queryID == "" {
			t.Fatal("query id in context is empty")
		}
		if w.Header().Get(DefaultQueryIDHeader) != queryID {
			t.Fatalf("response header query id = %q, want %q", w.Header().Get(DefaultQueryIDHeader), queryID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
