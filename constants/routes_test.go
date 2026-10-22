package constants

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/webapp"
)

func transportTestRouter(t *testing.T, manager *transportTestManager) http.Handler {
	t.Helper()
	name := "ConstantRoutesTransportTest"
	service := &Service{manager: manager, authorize: allowTransport}
	if err := webapp.RegisterService(name, service, func(webapp.ServiceContext) any { return service }); err != nil {
		t.Fatal(err)
	}
	routes, err := Routes("/api/constants/", name)
	if err != nil {
		t.Fatal(err)
	}
	return webapp.MustBuildRouter(&webapp.AppContext{}, routes)
}

func TestRoutesUseCorrectMethodsAndPreserveExactJSONNumbers(t *testing.T) {
	manager := &transportTestManager{}
	handler := transportTestRouter(t, manager)
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/constants?search=timeout&limit=5&offset=2&value_type=int64", ""},
		{http.MethodGet, "/api/constants/7", ""},
		{http.MethodPost, "/api/constants/get", `{"ids":[7]}`},
		{http.MethodPost, "/api/constants/set", `{"items":[{"id":7,"value":9223372036854775807,"expected_version":2}]}`},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
	if manager.list.Search != "timeout" || manager.list.Limit != 5 || manager.list.Offset != 2 || manager.list.ValueType != TypeInt64 {
		t.Fatalf("list options = %#v", manager.list)
	}
	if len(manager.changes) != 1 || string(manager.changes[0].Value) != "9223372036854775807" || *manager.changes[0].ExpectedVersion != 2 {
		t.Fatalf("assignment lost precision: %#v", manager.changes)
	}
	if !json.Valid(manager.changes[0].Value) {
		t.Fatal("invalid raw value")
	}
}

func TestRoutesRejectMalformedRequestsBeforeManagerAccess(t *testing.T) {
	manager := &transportTestManager{}
	handler := transportTestRouter(t, manager)
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/constants?limit=oops", ""},
		{http.MethodGet, "/api/constants?limit=1&limit=2", ""},
		{http.MethodGet, "/api/constants?unknown=yes", ""},
		{http.MethodGet, "/api/constants/nope", ""},
		{http.MethodGet, "/api/constants/0", ""},
		{http.MethodPost, "/api/constants/get", ""},
		{http.MethodPost, "/api/constants/get", `null`},
		{http.MethodPost, "/api/constants/get", `[]`},
		{http.MethodPost, "/api/constants/get", `{"ids":[7]} {}`},
		{http.MethodPost, "/api/constants/get", `{"ids":[7],"extra":true}`},
		{http.MethodPost, "/api/constants/get", `{"ids":[7.2]}`},
		{http.MethodPost, "/api/constants/get", `{"ids":[9223372036854775808]}`},
		{http.MethodPost, "/api/constants/set", `{"items":[{"id":7,"value":true,"extra":1}]}`},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s %s %s: %d %s", test.method, test.path, test.body, recorder.Code, recorder.Body.String())
		}
	}
	if manager.calls != 0 {
		t.Fatalf("malformed request reached manager %d times", manager.calls)
	}
}

func TestRoutesRejectOversizedBody(t *testing.T) {
	manager := &transportTestManager{}
	handler := transportTestRouter(t, manager)
	body := `{"items":[{"id":7,"value":"` + strings.Repeat("x", maxRequestBody) + `"}]}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/constants/set", strings.NewReader(body)))
	if recorder.Code != http.StatusRequestEntityTooLarge || manager.calls != 0 {
		t.Fatalf("oversized request: status=%d calls=%d", recorder.Code, manager.calls)
	}
}

func TestRoutesRejectInvalidPrefixes(t *testing.T) {
	for _, prefix := range []string{"", "/", "constants", "/api/{id}", "/api//constants", "/api/constants?x", "/api constants"} {
		if _, err := Routes(prefix, "Constant"); err == nil {
			t.Fatalf("prefix %q was accepted", prefix)
		}
	}
	if _, err := Routes("/api/constants", ""); err == nil {
		t.Fatal("empty service name was accepted")
	}
}
