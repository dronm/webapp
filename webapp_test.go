package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/modelbind"
)

type testInputModel struct {
	ID     *int    `json:"id" primaryKey:""`
	Name   *string `json:"name" alias:"Name" required:"" min:"2" max:"100"`
	Age    *int    `json:"age" alias:"Age" min:"18"`
	Active *bool   `json:"active"`
}

func (m *testInputModel) Relation() string {
	return "test_models"
}

type testService struct {
	svcCtx ServiceContext
}

func newTestService(ctx ServiceContext) any {
	return &testService{svcCtx: ctx}
}

func (s *testService) EchoInput(ctx context.Context, input modelbind.ModelInput[*testInputModel]) (map[string]any, error) {
	if input.Model == nil {
		return nil, errors.New("input model is nil")
	}

	name := ""
	if input.Model.Name != nil {
		name = *input.Model.Name
	}

	return map[string]any{
		"name":     name,
		"idAbsent": input.AbsentFields.IsAbsent("id"),
		"queryID":  s.svcCtx.QueryID,
	}, nil
}

func (s *testService) Detail(ctx context.Context, id int) (map[string]any, error) {
	return map[string]any{"id": id}, nil
}

func (s *testService) NoInput(ctx context.Context) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func (s *testService) OnlyError(ctx context.Context, input int) error {
	if input != 10 {
		return errors.New("unexpected input")
	}

	return nil
}

func (s *testService) HTTPError(ctx context.Context) (map[string]any, error) {
	return nil, Forbidden("permission denied", map[string]any{"permission": "Supplier.list"})
}

func (s *testService) InternalError(ctx context.Context) (map[string]any, error) {
	return nil, errors.New("private failure text")
}

func resetTestServices(t *testing.T) {
	t.Helper()

	serviceRegistryMu.Lock()
	defer serviceRegistryMu.Unlock()

	serviceRegistry = map[string]ServiceDef{}
}

func registerTestService(t *testing.T, name string) {
	t.Helper()

	if err := RegisterService(name, &testService{}, newTestService); err != nil {
		t.Fatalf("RegisterService() error = %v", err)
	}
}

func performRequest(handler http.Handler, method string, target string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(DefaultQueryIDHeader, "query-123")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSONBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, rec.Body.String())
	}

	return value
}

func TestBuildRouterCallsRegisteredServiceWithValidatedJSONInput(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	routes := []Route{
		POST(
			"/supplier/add",
			WithService("Supplier", "EchoInput"),
			WithBinder(InsertInputBinder[*testInputModel]()),
		),
	}
	handler := MustBuildRouter(&AppContext{}, routes, QueryIDMiddleware(""))

	rec := performRequest(handler, http.MethodPost, "/supplier/add", `{"name":"Alice","age":25}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONBody[map[string]any](t, rec)
	if body["name"] != "Alice" {
		t.Fatalf("name = %#v, want Alice", body["name"])
	}
	if body["idAbsent"] != true {
		t.Fatalf("idAbsent = %#v, want true", body["idAbsent"])
	}
	if body["queryID"] != "query-123" {
		t.Fatalf("queryID = %#v, want query-123", body["queryID"])
	}
}

func TestBuildRouterReturnsBadRequestForValidationError(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	routes := []Route{
		POST(
			"/supplier/add",
			WithService("Supplier", "EchoInput"),
			WithBinder(InsertInputBinder[*testInputModel]()),
		),
	}
	handler := MustBuildRouter(&AppContext{}, routes)

	rec := performRequest(handler, http.MethodPost, "/supplier/add", `{"age":25}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	body := decodeJSONBody[ErrorResponse](t, rec)
	if body.Error.Code != "bad_request" {
		t.Fatalf("error code = %q, want bad_request", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "Name") {
		t.Fatalf("message = %q, want field alias Name", body.Error.Message)
	}
}

func TestPathValueBinderCallsServiceWithScalarPathValue(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	routes := []Route{
		GET(
			"/supplier/{id}",
			WithService("Supplier", "Detail"),
			WithBinder(PathValueBinder[int]("id")),
		),
	}
	handler := MustBuildRouter(&AppContext{}, routes)

	rec := performRequest(handler, http.MethodGet, "/supplier/42", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONBody[map[string]any](t, rec)
	if body["id"] != float64(42) {
		t.Fatalf("id = %#v, want 42", body["id"])
	}
}

func TestServiceMethodReturningOnlyErrorWritesDefaultOKEnvelope(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	routes := []Route{
		POST(
			"/supplier/check/{id}",
			WithService("Supplier", "OnlyError"),
			WithBinder(PathValueBinder[int]("id")),
		),
	}
	handler := MustBuildRouter(&AppContext{}, routes)

	rec := performRequest(handler, http.MethodPost, "/supplier/check/10", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONBody[map[string]any](t, rec)
	if body["ok"] != true {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}
}

func TestServiceHTTPErrorIsReturnedAsPublicJSONError(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	routes := []Route{
		GET(
			"/supplier/list",
			WithService("Supplier", "HTTPError"),
		),
	}
	handler := MustBuildRouter(&AppContext{}, routes)

	rec := performRequest(handler, http.MethodGet, "/supplier/list", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	body := decodeJSONBody[ErrorResponse](t, rec)
	if body.Error.Code != "forbidden" {
		t.Fatalf("code = %q, want forbidden", body.Error.Code)
	}
	if body.Error.Message != "permission denied" {
		t.Fatalf("message = %q, want permission denied", body.Error.Message)
	}
}

func TestPlainServiceErrorDoesNotLeakPrivateErrorText(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	routes := []Route{
		GET(
			"/supplier/list",
			WithService("Supplier", "InternalError"),
		),
	}
	handler := MustBuildRouter(&AppContext{}, routes)

	rec := performRequest(handler, http.MethodGet, "/supplier/list", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private failure text") {
		t.Fatalf("response leaked private error text: %s", rec.Body.String())
	}
}
