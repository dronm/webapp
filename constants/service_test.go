package constants

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dronm/webapp"
)

type transportTestManager struct {
	calls   int
	list    ListOptions
	ids     []int64
	changes []Change
	err     error
}

func (m *transportTestManager) List(_ context.Context, options ListOptions) (ListResult, error) {
	m.calls++
	m.list = options
	return ListResult{Items: []Metadata{{ID: 7, Code: "visible"}}, Total: 1}, m.err
}

func (m *transportTestManager) Detail(_ context.Context, id int64) (Metadata, error) {
	m.calls++
	m.ids = []int64{id}
	return Metadata{ID: id, Code: "visible"}, m.err
}

func (m *transportTestManager) Get(_ context.Context, ids []int64) ([]Value, error) {
	m.calls++
	m.ids = append([]int64(nil), ids...)
	return []Value{{ID: ids[0], Code: "visible", Value: json.RawMessage("true"), Version: 2}}, m.err
}

func (m *transportTestManager) Set(_ context.Context, changes []Change) ([]Value, error) {
	m.calls++
	m.changes = changes
	return []Value{{ID: changes[0].ID, Code: "visible", Value: changes[0].Value, Version: 3}}, m.err
}

func allowTransport(context.Context, Operation, []int64) error { return nil }

func requireHTTPStatus(t *testing.T, err error, status int) webapp.HTTPError {
	t.Helper()
	var httpErr webapp.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode() != status {
		t.Fatalf("error = %v, want HTTP %d", err, status)
	}
	return httpErr
}

func TestServiceRequiresAuthorization(t *testing.T) {
	if _, err := NewService(nil, allowTransport); err == nil {
		t.Fatal("nil manager was accepted")
	}
	if _, err := NewService(&Manager{}, nil); err == nil {
		t.Fatal("nil authorizer was accepted")
	}
	var zero Service
	_, err := zero.Get(context.Background(), GetInput{IDs: []int64{1}})
	requireHTTPStatus(t, err, http.StatusInternalServerError)
}

func TestServiceAuthorizesBeforeAnyManagerAccess(t *testing.T) {
	tests := []struct {
		operation Operation
		ids       []int64
		call      func(*Service) error
	}{
		{OperationList, nil, func(s *Service) error { _, err := s.List(context.Background(), ListOptions{}); return err }},
		{OperationDetail, []int64{7}, func(s *Service) error { _, err := s.Detail(context.Background(), DetailInput{ID: 7}); return err }},
		{OperationGet, []int64{7, 8}, func(s *Service) error {
			_, err := s.Get(context.Background(), GetInput{IDs: []int64{7, 8}})
			return err
		}},
		{OperationSet, []int64{7}, func(s *Service) error {
			_, err := s.Set(context.Background(), SetInput{Items: []Change{{ID: 7, Value: json.RawMessage("true")}}})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			manager := &transportTestManager{}
			service := &Service{manager: manager, authorize: func(_ context.Context, operation Operation, ids []int64) error {
				if operation != test.operation || !reflect.DeepEqual(ids, test.ids) {
					t.Fatalf("authorize(%s, %v), want (%s, %v)", operation, ids, test.operation, test.ids)
				}
				return errors.New("private authorization failure")
			}}
			err := test.call(service)
			httpErr := requireHTTPStatus(t, err, http.StatusForbidden)
			if strings.Contains(httpErr.PublicMessage(), "private") || manager.calls != 0 {
				t.Fatalf("authorization leaked or manager accessed: message=%q calls=%d", httpErr.PublicMessage(), manager.calls)
			}
		})
	}
}

func TestServicePreservesAuthorizationHTTPError(t *testing.T) {
	want := webapp.Unauthorized("Sign in", nil)
	service := &Service{manager: &transportTestManager{}, authorize: func(context.Context, Operation, []int64) error { return want }}
	_, err := service.List(context.Background(), ListOptions{})
	if err != want {
		t.Fatalf("error = %v, want original %v", err, want)
	}
}

func TestServiceAuthorizationCannotMutateRequestedIDs(t *testing.T) {
	manager := &transportTestManager{}
	service := &Service{manager: manager, authorize: func(_ context.Context, _ Operation, ids []int64) error {
		ids[0] = 99
		return nil
	}}
	input := GetInput{IDs: []int64{7}}
	if _, err := service.Get(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if input.IDs[0] != 7 || !reflect.DeepEqual(manager.ids, []int64{7}) {
		t.Fatalf("requested IDs changed: input=%v manager=%v", input.IDs, manager.ids)
	}
}

func TestServiceValidatesListAndSetRequests(t *testing.T) {
	manager := &transportTestManager{}
	service := &Service{manager: manager, authorize: allowTransport}
	if _, err := service.List(context.Background(), ListOptions{Search: "  timeout  "}); err != nil {
		t.Fatal(err)
	}
	if manager.list.Limit != 100 || manager.list.Search != "timeout" {
		t.Fatalf("list options = %#v", manager.list)
	}
	for _, input := range []ListOptions{{Limit: -1}, {Limit: 501}, {Offset: -1}, {ValueType: "unknown"}} {
		_, err := service.List(context.Background(), input)
		requireHTTPStatus(t, err, http.StatusBadRequest)
	}
	negativeVersion := int64(-1)
	for _, input := range []SetInput{
		{},
		{Items: []Change{{ID: 0, Value: json.RawMessage("true")}}},
		{Items: []Change{{ID: 7}}},
		{Items: []Change{{ID: 7, Value: json.RawMessage("true")}, {ID: 7, Value: json.RawMessage("false")}}},
		{Items: []Change{{ID: 7, Value: json.RawMessage("true"), ExpectedVersion: &negativeVersion}}},
	} {
		before := manager.calls
		_, err := service.Set(context.Background(), input)
		requireHTTPStatus(t, err, http.StatusBadRequest)
		if manager.calls != before {
			t.Fatal("invalid assignment reached manager")
		}
	}
}

func TestTransportErrorMappingDoesNotExposePrivateCauses(t *testing.T) {
	tests := []struct {
		err    error
		status int
	}{
		{constantError(ErrNotFound, 7, "example", "private"), http.StatusNotFound},
		{constantError(ErrConflict, 7, "example", "private"), http.StatusConflict},
		{constantError(ErrValidation, 7, "example", "private"), http.StatusBadRequest},
		{ErrNotStarted, http.StatusServiceUnavailable},
		{ErrClosed, http.StatusServiceUnavailable},
		{ErrUnsynced, http.StatusServiceUnavailable},
		{errors.Join(ErrUnsynced, ErrNotFound), http.StatusServiceUnavailable},
		{errors.Join(ErrUnsynced, ErrValidation), http.StatusServiceUnavailable},
		{constantError(ErrDefinition, 7, "example", "private"), http.StatusInternalServerError},
		{errors.New("private SQL and database password"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		err := transportError(test.err)
		requireHTTPStatus(t, err, test.status)
		if !errors.Is(err, test.err) {
			t.Fatal("wrapped error lost cause")
		}
		recorder := httptest.NewRecorder()
		webapp.WriteServiceError(recorder, err)
		if recorder.Code != test.status || strings.Contains(recorder.Body.String(), "private") {
			t.Fatalf("unsafe response: %d %s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestRegisterServiceProtectsWebSocketCalls(t *testing.T) {
	name := "ConstantAuthorizationTransportTest"
	checks := 0
	err := RegisterService(name, &Manager{}, func(_ context.Context, operation Operation, ids []int64) error {
		checks++
		if operation != OperationGet || !reflect.DeepEqual(ids, []int64{7}) {
			t.Fatalf("unexpected authorization: %s %v", operation, ids)
		}
		return webapp.Unauthorized("Sign in", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = webapp.CallServiceJSON(context.Background(), name, "Get", json.RawMessage(`{"ids":[7]}`), webapp.ServiceContext{})
	requireHTTPStatus(t, err, http.StatusUnauthorized)
	if checks != 1 {
		t.Fatalf("authorization calls = %d, want 1", checks)
	}
	_, err = webapp.CallServiceJSON(context.Background(), name, "Get", json.RawMessage(`{"ids":[7],"ignored":true}`), webapp.ServiceContext{})
	requireHTTPStatus(t, err, http.StatusBadRequest)
	if checks != 1 {
		t.Fatal("invalid input reached the service")
	}
}

type transportIntegrationRepository struct {
	mu      sync.Mutex
	record  Record
	loads   int
	updates int
}

func (r *transportIntegrationRepository) LoadAll(context.Context) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads++
	return []Record{cloneRecord(r.record)}, nil
}

func (r *transportIntegrationRepository) Load(_ context.Context, ids []int64) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads++
	for _, id := range ids {
		if id == r.record.ID {
			return []Record{cloneRecord(r.record)}, nil
		}
	}
	return nil, nil
}

func (r *transportIntegrationRepository) Update(_ context.Context, writes []Write) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(writes) != 1 {
		return nil, errors.New("unexpected batch size")
	}
	write := writes[0]
	if write.ID != r.record.ID || write.Code != r.record.Code || write.ValueType != r.record.ValueType || write.ExpectedVersion != r.record.Version {
		return nil, ErrConflict
	}
	r.updates++
	r.record.Value = append(json.RawMessage(nil), write.Value...)
	r.record.Version++
	return []Record{cloneRecord(r.record)}, nil
}

func (r *transportIntegrationRepository) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loads, r.updates
}

func TestTransportAndTypedAccessShareManagerValidationAndCache(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, Definition[int64]{
		Code: "limit", ValueType: TypeInt64, Required: true,
		Validate: func(_ context.Context, value int64) error {
			if value < 0 || value > 100 {
				return errors.New("limit must be between zero and one hundred")
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	repository := &transportIntegrationRepository{record: Record{
		Metadata: Metadata{ID: 7, Code: "limit", Name: "Limit", ValueType: TypeInt64, Version: 1},
		Value:    json.RawMessage("10"),
	}}
	manager, err := NewManager(Config{Repository: repository, Registry: registry, ReconcileInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	type authenticatedKey struct{}
	authorize := func(ctx context.Context, _ Operation, _ []int64) error {
		if allowed, _ := ctx.Value(authenticatedKey{}).(bool); !allowed {
			return webapp.Unauthorized("Sign in", nil)
		}
		return nil
	}
	name := "ConstantManagerIntegrationTransportTest"
	if err := RegisterService(name, manager, authorize); err != nil {
		t.Fatal(err)
	}
	routes, err := Routes("/constants", name)
	if err != nil {
		t.Fatal(err)
	}
	handler := webapp.MustBuildRouter(&webapp.AppContext{}, routes)
	admin := context.WithValue(ctx, authenticatedKey{}, true)
	request := func(ctx context.Context, path, body string, status int) {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
		handler.ServeHTTP(recorder, req)
		if recorder.Code != status {
			t.Fatalf("%s: %d %s, want %d", path, recorder.Code, recorder.Body.String(), status)
		}
	}
	request(ctx, "/constants/get", `{"ids":[7]}`, http.StatusUnauthorized)
	request(admin, "/constants/get", `{"ids":[7]}`, http.StatusOK)
	if loads, updates := repository.counts(); loads != 1 || updates != 0 {
		t.Fatalf("cached reads reached repository: loads=%d updates=%d", loads, updates)
	}
	request(admin, "/constants/set", `{"items":[{"id":7,"value":1000}]}`, http.StatusBadRequest)
	if _, updates := repository.counts(); updates != 0 {
		t.Fatal("generic API bypassed project validator")
	}
	request(admin, "/constants/set", `{"items":[{"id":7,"value":20,"expected_version":1}]}`, http.StatusOK)
	request(admin, "/constants/set", `{"items":[{"id":7,"value":30,"expected_version":1}]}`, http.StatusConflict)
	accessor, err := NewConstant[int64](manager, "limit")
	if err != nil {
		t.Fatal(err)
	}
	loadsBefore, _ := repository.counts()
	value, version, err := accessor.GetVersioned(ctx)
	if err != nil || value != 20 || version != 2 {
		t.Fatalf("typed value = %d version=%d error=%v", value, version, err)
	}
	result, err := webapp.CallServiceJSON(admin, name, "Get", json.RawMessage(`{"ids":[7]}`), webapp.ServiceContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.([]Value); len(got) != 1 || string(got[0].Value) != "20" {
		t.Fatalf("WebSocket result = %#v", result)
	}
	if loadsAfter, updates := repository.counts(); loadsAfter != loadsBefore || updates != 1 {
		t.Fatalf("typed/WS read bypassed shared cache: loads=%d before=%d updates=%d", loadsAfter, loadsBefore, updates)
	}
}
