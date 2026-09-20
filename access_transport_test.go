package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	"github.com/dronm/session"
)

type accessTransportService struct{ conn *accessCaptureConn }

func (s *accessTransportService) Create(ctx context.Context, input modelbind.ModelInput[*accessOrder]) (map[string]any, error) {
	if _, err := InsertModelInputWithConn(ctx, s.conn, input, nil); err != nil {
		return nil, err
	}
	return map[string]any{"customer_id": *input.Model.CustomerID, "name": *input.Model.Name}, nil
}
func (s *accessTransportService) Update(ctx context.Context, input UpdateByKeysInput[*accessOrderKey, *accessOrder]) (int64, error) {
	return UpdateModelInputWithConn(ctx, s.conn, input.Keys, input.Input, nil)
}
func (s *accessTransportService) Read(ctx context.Context, id int) error {
	_, _, err := prepareDetailModelSelect(ctx, &accessOrderKey{ID: &id}, &accessOrder{})
	return err
}

type accessPublisher struct{ count int }

func (p *accessPublisher) PublishEvent(string, string, any) error { p.count++; return nil }

type accessScopedPublisher struct {
	accessPublisher
	scoped    int
	principal string
}

func (p *accessScopedPublisher) PublishAccessEvent(_ context.Context, principal Principal, _ string, _ ServiceMethodEvent) error {
	p.scoped++
	p.principal = principal.UserID
	return nil
}

func registerAccessTransport(t *testing.T, conn *accessCaptureConn, options ...ServiceOption) {
	t.Helper()
	resetTestServices(t)
	if err := RegisterService("AccessOrder", &accessTransportService{}, func(ServiceContext) any { return &accessTransportService{conn: conn} }, options...); err != nil {
		t.Fatal(err)
	}
}

func TestAccessHTTPBinderSuppliesOwnershipBeforeValidation(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn)
	controller := accessTestController(t)
	router := BuildRouter(&AppContext{Access: controller}, []Route{POST("/orders", WithService("AccessOrder", "Create"), WithBinder(InsertInputBinder[*accessOrder]()))})
	response := performRequest(router, http.MethodPost, "/orders", `{"name":"created"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"customer_id":42`) || len(conn.queries) != 1 {
		t.Fatal(response.Body.String(), conn.queries)
	}
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"name":"created","customer_id":99}`, http.StatusForbidden},
		{`{"name":"created","customer_id":null}`, http.StatusForbidden},
		{`{}`, http.StatusBadRequest},
	} {
		response = performRequest(router, http.MethodPost, "/orders", test.body)
		if response.Code != test.status {
			t.Fatalf("%s: %d %s", test.body, response.Code, response.Body.String())
		}
	}
	if len(conn.queries) != 1 {
		t.Fatal("rejected requests executed SQL")
	}
}

func TestAccessHTTPAuthorizationErrorsAreNotBinding400(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn)
	for _, test := range []struct {
		name      string
		configure func(*AccessController)
		status    int
	}{
		{"anonymous", func(c *AccessController) { c.ResolvePrincipal = nil }, http.StatusUnauthorized},
		{"lookup failure", func(c *AccessController) {
			c.ResolvePrincipal = func(context.Context, session.Session) (Principal, error) {
				return Principal{}, errors.New("private database lookup")
			}
		}, http.StatusInternalServerError},
		{"missing binding", func(c *AccessController) { c.Registry = NewPolicyRegistry() }, http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := accessTestController(t)
			test.configure(controller)
			router := BuildRouter(&AppContext{Access: controller}, []Route{POST("/orders", WithService("AccessOrder", "Create"), WithBinder(InsertInputBinder[*accessOrder]()))})
			response := performRequest(router, http.MethodPost, "/orders", `{"name":"test"}`)
			if response.Code != test.status || strings.Contains(response.Body.String(), "private database") {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
	if len(conn.queries) != 0 {
		t.Fatal("unauthorized SQL executed")
	}
}

func TestAccessHTTPBinderAndCRUDShareOnePolicySnapshot(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn)
	controller := accessTestController(t)
	var calls atomic.Int64
	controller.ResolvePrincipal = func(context.Context, session.Session) (Principal, error) {
		calls.Add(1)
		return Principal{UserID: "7", Roles: []string{"customer"}, Claims: map[string]any{"customer_id": 42}}, nil
	}
	router := BuildRouter(&AppContext{Access: controller}, []Route{POST("/orders", WithService("AccessOrder", "Create"), WithBinder(InsertInputBinder[*accessOrder]()))})
	for i := 0; i < 2; i++ {
		if response := performRequest(router, http.MethodPost, "/orders", `{"name":"test"}`); response.Code != 200 {
			t.Fatal(response.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("identity resolutions=%d, want one per request", calls.Load())
	}
}

func TestAccessKeyBindersDoNotApplyWritePolicy(t *testing.T) {
	r := accessTestRegistry(t)
	if err := r.Register("Order", access.Read, func(context.Context, Principal) (access.Policy, error) {
		return access.Scope(access.Eq("customer_id", 42)), nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithPrincipal(ContextWithAccess(context.Background(), &AccessController{Registry: r}), Principal{UserID: "7"})
	request := httptest.NewRequest(http.MethodGet, "/orders/5", nil).WithContext(ctx)
	request.SetPathValue("id", "5")
	value, err := PathInputBinder[*accessOrderKey](true, "id")(request)
	if err != nil {
		t.Fatal(err)
	}
	key := value.(modelbind.ModelInput[*accessOrderKey]).Model
	if _, _, err := prepareDetailModelSelect(ctx, key, &accessOrder{}); err != nil {
		t.Fatal(err)
	}
}

func TestAccessCustomHTTPHandlerReceivesContext(t *testing.T) {
	router := BuildRouter(&AppContext{Access: accessTestController(t)}, []Route{GET("/custom", WithHandler(func(w http.ResponseWriter, r *http.Request) {
		if _, err := ResolveModelAccess(r.Context(), &accessOrder{}, access.Read); err != nil {
			WriteServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))})
	response := performRequest(router, http.MethodGet, "/custom", "")
	if response.Code != http.StatusNoContent {
		t.Fatal(response.Body.String())
	}
}

func TestAccessJSONDispatchTracksAbsenceAndRefreshesEachCall(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn, WithMethodInputDecoder("Create", ModelInputJSONDecoder[*accessOrder](true)), WithMethodInputDecoder("Update", UpdateByKeysJSONDecoder[*accessOrderKey, *accessOrder]()))
	controller := accessTestController(t)
	customer := 42
	calls := 0
	controller.ResolvePrincipal = func(context.Context, session.Session) (Principal, error) {
		calls++
		return Principal{UserID: "7", Roles: []string{"customer"}, Claims: map[string]any{"customer_id": customer}}, nil
	}
	svcCtx := ServiceContext{Access: controller, QueryID: "ws-query"}
	result, err := CallServiceJSON(context.Background(), "AccessOrder", "Create", json.RawMessage(`{"name":"first"}`), svcCtx)
	if err != nil || result.(map[string]any)["customer_id"] != 42 {
		t.Fatalf("%#v %v", result, err)
	}
	customer = 99
	result, err = CallServiceJSON(context.Background(), "AccessOrder", "Create", json.RawMessage(`{"name":"second"}`), svcCtx)
	if err != nil || result.(map[string]any)["customer_id"] != 99 || calls != 2 {
		t.Fatalf("%#v %v calls=%d", result, err, calls)
	}
	_, err = CallServiceJSON(context.Background(), "AccessOrder", "Update", json.RawMessage(`{"keys":{"id":1},"input":{"name":"patch"}}`), svcCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conn.queries[2], "SET name = $1 WHERE (id = $2) AND (customer_id = $3)") {
		t.Fatal(conn.queries[2])
	}
	_, err = CallServiceJSON(context.Background(), "AccessOrder", "Update", json.RawMessage(`{"keys":{},"input":{"name":"patch"}}`), svcCtx)
	if !errors.Is(err, modelbind.ErrMissingKey) {
		t.Fatal(err)
	}
}

func TestAccessJSONDecoderCannotAcceptClientPresenceMetadata(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn, WithMethodInputDecoder("Create", ModelInputJSONDecoder[*accessOrder](true)))
	svcCtx := ServiceContext{Access: accessTestController(t)}
	_, err := CallServiceJSON(context.Background(), "AccessOrder", "Create", json.RawMessage(`{"name":"test","customer_id":99,"AbsentFields":{"customer_id":true}}`), svcCtx)
	if !errors.Is(err, access.ErrWriteConflict) {
		t.Fatal(err)
	}
	if len(conn.queries) != 0 {
		t.Fatal("spoofed presence executed SQL")
	}
	_, err = CallServiceJSON(context.Background(), "AccessOrder", "Create", json.RawMessage(`{"Model":{"name":"test"},"AbsentFields":{}}`), svcCtx)
	if err == nil {
		t.Fatal("ModelInput envelope accepted as flat model")
	}
}

func TestAccessProtectedJSONInputRequiresTypedDecoder(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn)
	_, err := CallServiceJSON(context.Background(), "AccessOrder", "Create", json.RawMessage(`{"name":"test"}`), ServiceContext{Access: accessTestController(t)})
	if !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
	if len(conn.queries) != 0 {
		t.Fatal("missing decoder bypassed input preparation")
	}
	resetTestServices(t)
	err = RegisterService("WrongDecoder", &accessTransportService{}, func(ServiceContext) any { return &accessTransportService{} }, WithMethodInputDecoder("Missing", ModelInputJSONDecoder[*accessOrder](true)))
	if err == nil {
		t.Fatal("unknown decoder method accepted")
	}
}

func TestAccessNotificationsRequireRecipientAwarePublisher(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn, WithCRUDNotifications(), WithMethodInputDecoder("Create", ModelInputJSONDecoder[*accessOrder](true)))
	ordinary := &accessPublisher{}
	svcCtx := ServiceContext{Access: accessTestController(t), EventPublisher: ordinary}
	if _, err := CallServiceJSON(context.Background(), "AccessOrder", "Create", json.RawMessage(`{"name":"secret"}`), svcCtx); err != nil {
		t.Fatal(err)
	}
	if ordinary.count != 0 {
		t.Fatal("protected payload broadcast to ordinary publisher")
	}
	authorized := &accessScopedPublisher{}
	svcCtx.EventPublisher = authorized
	if _, err := CallServiceJSON(context.Background(), "AccessOrder", "Create", json.RawMessage(`{"name":"secret"}`), svcCtx); err != nil {
		t.Fatal(err)
	}
	if authorized.count != 0 || authorized.scoped != 1 || authorized.principal != "7" {
		t.Fatalf("%#v", authorized)
	}
}

type accessTestSession struct {
	session.Session
	id string
}

func (s *accessTestSession) SessionID() string { return s.id }

func TestAccessSessionChangeInvalidatesUpgradeIdentity(t *testing.T) {
	controller := accessTestController(t)
	calls := 0
	controller.ResolvePrincipal = func(_ context.Context, s session.Session) (Principal, error) {
		calls++
		return Principal{UserID: s.SessionID(), Roles: []string{"customer"}, Claims: map[string]any{"customer_id": 42}}, nil
	}
	ctx := ContextWithPrincipal(ContextWithAccess(context.Background(), controller), Principal{UserID: "upgrade", Roles: []string{"customer"}, Claims: map[string]any{"customer_id": 99}})
	ctx = ContextWithSession(ctx, &accessTestSession{id: "message"})
	principal, err := PrincipalFromContext(ctx)
	if err != nil || principal.UserID != "message" || calls != 1 {
		t.Fatalf("%#v %v %d", principal, err, calls)
	}
	ctx = PrepareServiceContext(ctx, ServiceContext{Access: controller, Session: &accessTestSession{id: "changed"}})
	principal, err = PrincipalFromContext(ctx)
	if err != nil || principal.UserID != "changed" || calls != 2 {
		t.Fatalf("%#v %v %d", principal, err, calls)
	}
}

func TestAccessRoutePermissionIsStillIndependent(t *testing.T) {
	conn := &accessCaptureConn{affected: 1}
	registerAccessTransport(t, conn)
	controller := accessTestController(t)
	calls := 0
	controller.ResolvePrincipal = func(context.Context, session.Session) (Principal, error) { calls++; return Principal{}, nil }
	router := BuildRouter(&AppContext{Access: controller, PermissionChecker: func(*http.Request, string) error { return Forbidden("method denied", nil) }}, []Route{
		POST("/orders", WithPermission("Order.create"), WithService("AccessOrder", "Create"), WithBinder(InsertInputBinder[*accessOrder]())),
	})
	response := performRequest(router, http.MethodPost, "/orders", `{"name":"test"}`)
	if response.Code != http.StatusForbidden || calls != 0 || len(conn.queries) != 0 {
		t.Fatalf("%d calls=%d queries=%d", response.Code, calls, len(conn.queries))
	}
}
