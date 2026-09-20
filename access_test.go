package webapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
	"github.com/dronm/session"
)

type accessOrder struct {
	ID         *int    `json:"id" primaryKey:""`
	CustomerID *int    `json:"customer_id" required:""`
	Name       *string `json:"name" required:""`
}

func (accessOrder) Relation() string       { return "orders" }
func (accessOrder) AccessResource() string { return "Order" }

type accessOrderKey struct {
	ID *int `json:"id" required:""`
}

func (accessOrderKey) Relation() string       { return "orders" }
func (accessOrderKey) AccessResource() string { return "Order" }

type accessCompositeKey struct {
	DocumentID *int `json:"document_id" required:""`
	LineNo     *int `json:"line_no" required:""`
}

func (accessCompositeKey) Relation() string       { return "orders" }
func (accessCompositeKey) AccessResource() string { return "Order" }

type accessHiddenOrder struct {
	Name *string `json:"name" required:""`
}

func (accessHiddenOrder) Relation() string       { return "orders" }
func (accessHiddenOrder) AccessResource() string { return "Order" }

type accessOrderList struct {
	Name string `json:"name"`
}

func (accessOrderList) Relation() string       { return "orders AS o" }
func (accessOrderList) AccessResource() string { return "Order" }
func (accessOrderList) CollectionAgg() any {
	return &struct {
		Count int64 `json:"count" agg:"count(*)"`
	}{}
}

type accessUnmarkedOrder struct {
	ID int `json:"id"`
}

func (accessUnmarkedOrder) Relation() string { return "orders" }

type accessCaptureConn struct {
	ds.Conn
	queries  []string
	params   [][]any
	affected int64
	execErr  error
}

func (c *accessCaptureConn) Exec(_ context.Context, query string, params ...any) (ds.ExecResult, error) {
	c.queries = append(c.queries, query)
	c.params = append(c.params, append([]any(nil), params...))
	return accessExecResult{affected: c.affected}, c.execErr
}

type accessExecResult struct {
	ds.ExecResult
	affected int64
}

func (r accessExecResult) RowsAffected() int64 { return r.affected }
func accessPtr[T any](value T) *T              { return &value }

func accessTestRegistry(t *testing.T) *PolicyRegistry {
	t.Helper()
	r := NewPolicyRegistry()
	binding := modelpg.PolicyBinding{
		Rows:   modelpg.FieldMap{"customer_id": "customer_id", "id": "id", "name": "name"},
		Writes: modelpg.FieldMap{"customer_id": "customer_id", "name": "name"},
	}
	for _, model := range []types.DBModel{accessOrder{}, accessOrderKey{}, accessCompositeKey{}, accessHiddenOrder{}} {
		if err := r.RegisterModel(model, binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.RegisterModel(accessOrderList{}, modelpg.PolicyBinding{Rows: modelpg.FieldMap{"customer_id": "o.customer_id", "name": "o.name", "id": "o.id"}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func accessTestController(t *testing.T) *AccessController {
	t.Helper()
	r := accessTestRegistry(t)
	for _, operation := range []access.Operation{access.Read, access.Insert, access.Update, access.Delete} {
		operation := operation
		if err := r.RegisterRole("customer", "Order", operation, func(_ context.Context, p Principal) (access.Policy, error) {
			id := p.Claims["customer_id"]
			if operation == access.Insert {
				return access.ForInsert(access.WriteRules{"customer_id": access.Enforce(id)}), nil
			}
			policy := access.Scope(access.Eq("customer_id", id))
			if operation == access.Update {
				policy.Writes = access.WriteRules{"customer_id": access.Enforce(id)}
			}
			return policy, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return &AccessController{Registry: r, ResolvePrincipal: func(context.Context, session.Session) (Principal, error) {
		return Principal{UserID: "7", Roles: []string{"customer"}, Claims: map[string]any{"customer_id": 42}}, nil
	}}
}
func accessTestContext(t *testing.T) context.Context {
	return ContextWithAccess(context.Background(), accessTestController(t))
}
func accessDecode(t *testing.T, raw string) modelbind.ModelInput[*accessOrder] {
	t.Helper()
	input, err := modelbind.DecodeJSONBodyInput[*accessOrder](strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestAccessAllCRUDPathsFailWithoutContext(t *testing.T) {
	ctx := context.Background()
	model := &accessOrder{Name: accessPtr("test"), CustomerID: accessPtr(42)}
	input := modelbind.ModelInput[*accessOrder]{Model: model}
	key := &accessOrderKey{ID: accessPtr(1)}
	checks := []func() error{
		func() error { _, err := InsertModelInput(ctx, nil, input, nil); return err },
		func() error { _, err := InsertModelInputWithConn(ctx, nil, input, nil); return err },
		func() error { _, err := InsertModel(ctx, nil, model, nil); return err },
		func() error { _, err := InsertModelWithConn(ctx, nil, model, nil); return err },
		func() error { _, err := UpdateModelInput(ctx, nil, key, input, nil); return err },
		func() error { _, err := UpdateModelInputWithConn(ctx, nil, key, input, nil); return err },
		func() error { _, err := UpdateModel(ctx, nil, key, model, nil); return err },
		func() error { _, err := UpdateModelWithConn(ctx, nil, key, model, nil); return err },
		func() error { _, err := DeleteModel(ctx, nil, []types.DBModel{key}, nil); return err },
		func() error { _, err := DeleteModelWithConn(ctx, nil, []types.DBModel{key}, nil); return err },
		func() error { _, err := FetchModel(ctx, nil, key, model); return err },
		func() error { return FetchModelWithConn(ctx, nil, key, model) },
		func() error {
			_, _, err := FetchCollectionModel(ctx, nil, &accessOrderList{}, modelbind.CollectionParams{})
			return err
		},
		func() error {
			_, _, err := FetchCollectionModelWithConn(ctx, nil, &accessOrderList{}, modelbind.CollectionParams{})
			return err
		},
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, ErrAccessConfiguration) {
			t.Errorf("path %d: %v", i, err)
		}
	}
}

func TestAccessMissingPrincipalPoliciesAndBindings(t *testing.T) {
	controller := accessTestController(t)
	controller.ResolvePrincipal = nil
	_, err := ResolveModelAccess(ContextWithAccess(context.Background(), controller), &accessOrder{}, access.Read)
	if !errors.Is(err, ErrPrincipalRequired) {
		t.Fatal(err)
	}
	ctx := ContextWithPrincipal(ContextWithAccess(context.Background(), controller), Principal{UserID: "8", Roles: []string{"other"}})
	if _, err := ResolveModelAccess(ctx, &accessOrder{}, access.Read); !errors.Is(err, access.ErrDenied) {
		t.Fatal(err)
	}
	controller.Registry = NewPolicyRegistry()
	ctx = ContextWithPrincipal(ContextWithAccess(context.Background(), controller), Principal{UserID: "8"})
	if _, err := ResolveModelAccess(ctx, &accessOrder{}, access.Read); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
	if err := controller.Registry.RegisterModel(accessOrder{}, modelpg.PolicyBinding{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveModelAccess(ctx, &accessOrder{}, access.Read); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
	if _, err := ResolveModelAccess(accessTestContext(t), &accessUnmarkedOrder{}, access.Read); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
	legacy, err := ResolveModelAccess(context.Background(), &testInputModel{}, access.Read)
	if err != nil || legacy.Applied {
		t.Fatalf("legacy unexpectedly scoped: %#v %v", legacy, err)
	}
}

func TestAccessRoleUserRestrictionsIntersect(t *testing.T) {
	controller := accessTestController(t)
	if err := controller.Registry.RegisterUser("7", "Order", access.Read, func(context.Context, Principal) (access.Policy, error) {
		return access.Scope(access.In("id", 1, 2)), nil
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveModelAccess(ContextWithAccess(context.Background(), controller), &accessOrder{}, access.Read)
	if err != nil {
		t.Fatal(err)
	}
	params := []any{}
	sql, err := modelpg.CompilePredicate(resolved.Policy.Rows, resolved.Binding.Rows, &params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "customer_id = $1") || !strings.Contains(sql, "AND (id IN ($2,$3))") {
		t.Fatal(sql)
	}
	if !reflect.DeepEqual(params, []any{int64(42), int64(1), int64(2)}) {
		t.Fatal(params)
	}
	if _, err := IntersectAccessPolicies(access.Read, access.AllowAll(), access.DenyAll()); !errors.Is(err, access.ErrDenied) {
		t.Fatal(err)
	}
	if _, err := IntersectAccessPolicies(access.Insert, access.ForInsert(access.WriteRules{"customer_id": access.Enforce(42)}), access.ForInsert(access.WriteRules{"customer_id": access.Enforce(99)})); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
}

func TestAccessResolutionCachedAndIsolated(t *testing.T) {
	controller := accessTestController(t)
	var calls atomic.Int64
	controller.ResolvePrincipal = func(context.Context, session.Session) (Principal, error) {
		calls.Add(1)
		return Principal{UserID: "7", Roles: []string{"customer", "customer"}, Claims: map[string]any{"customer_id": 42}}, nil
	}
	ctx := ContextWithAccess(context.Background(), controller)
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolved, err := ResolveModelAccess(ctx, &accessOrder{}, access.Read)
			if err != nil {
				t.Error(err)
				return
			}
			resolved.Binding.Rows["customer_id"] = "changed"
			principal, err := PrincipalFromContext(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			principal.Claims["customer_id"] = 999
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	_, err := ResolveModelAccess(ContextWithAccess(ctx, controller), &accessOrder{}, access.Read)
	if err != nil || calls.Load() != 2 {
		t.Fatalf("fresh scope: %d %v", calls.Load(), err)
	}
	resolved, err := ResolveModelAccess(ctx, &accessOrder{}, access.Read)
	if err != nil || resolved.Binding.Rows["customer_id"] != "customer_id" {
		t.Fatalf("binding mutated: %#v %v", resolved, err)
	}
}

func TestAccessLookupFailureStaysDenied(t *testing.T) {
	controller := accessTestController(t)
	calls := 0
	lookupErr := errors.New("private identity SQL")
	controller.ResolvePrincipal = func(context.Context, session.Session) (Principal, error) { calls++; return Principal{}, lookupErr }
	ctx := ContextWithAccess(context.Background(), controller)
	for i := 0; i < 2; i++ {
		_, err := InsertModelInputWithConn(ctx, nil, accessDecode(t, `{"name":"test"}`), nil)
		if !errors.Is(err, lookupErr) || !errors.Is(err, ErrPolicyResolution) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestAccessPrepareInputAndDirectInsert(t *testing.T) {
	ctx := accessTestContext(t)
	input := accessDecode(t, `{"name":"new"}`)
	effective, err := PrepareAccessInput(ctx, input, access.Insert)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Model.CustomerID == nil || *effective.Model.CustomerID != 42 || effective.IsAbsent("customer_id") {
		t.Fatalf("effective: %#v", effective)
	}
	if input.Model.CustomerID != nil || input.IsPresent("customer_id") {
		t.Fatal("submitted input mutated")
	}
	conn := &accessCaptureConn{affected: 1}
	if _, err := InsertModelInputWithConn(ctx, conn, input, nil); err != nil {
		t.Fatal(err)
	}
	if len(conn.queries) != 1 || !strings.Contains(conn.queries[0], "customer_id") {
		t.Fatal(conn.queries)
	}
	if input.Model.CustomerID != nil {
		t.Fatal("CRUD mutated caller")
	}
	for _, raw := range []string{`{"name":"new","customer_id":99}`, `{"name":"new","customer_id":null}`} {
		_, err := InsertModelInputWithConn(ctx, conn, accessDecode(t, raw), nil)
		if !errors.Is(err, access.ErrWriteConflict) {
			t.Fatal(err)
		}
	}
	if len(conn.queries) != 1 {
		t.Fatal("conflicting writes executed")
	}
	if _, err := InsertModelWithConn(ctx, conn, &accessOrder{Name: accessPtr("x"), CustomerID: accessPtr(99)}, nil); !errors.Is(err, access.ErrWriteConflict) {
		t.Fatal(err)
	}
}

func TestAccessHiddenWriteColumnIsStillEnforced(t *testing.T) {
	ctx := accessTestContext(t)
	conn := &accessCaptureConn{affected: 1}
	input := modelbind.ModelInput[*accessHiddenOrder]{Model: &accessHiddenOrder{Name: accessPtr("hidden owner")}}
	if _, err := InsertModelInputWithConn(ctx, conn, input, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conn.queries[0], "(name,customer_id)") || conn.params[0][1] != int64(42) {
		t.Fatalf("%s %#v", conn.queries[0], conn.params[0])
	}
}

func TestAccessUpdateGuardsOldRowAndSubmittedValues(t *testing.T) {
	ctx := accessTestContext(t)
	conn := &accessCaptureConn{affected: 1}
	key := &accessOrderKey{ID: accessPtr(10)}
	input := accessDecode(t, `{"name":"changed"}`)
	if _, err := UpdateModelInputWithConn(ctx, conn, key, input, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conn.queries[0], "SET name = $1 WHERE (id = $2) AND (customer_id = $3)") {
		t.Fatal(conn.queries[0])
	}
	if input.IsPresent("customer_id") {
		t.Fatal("absence mutated")
	}
	if _, err := UpdateModelInputWithConn(ctx, conn, key, accessDecode(t, `{"customer_id":99}`), nil); !errors.Is(err, access.ErrWriteConflict) {
		t.Fatal(err)
	}
	if _, err := UpdateModelWithConn(ctx, conn, key, &accessOrder{Name: accessPtr("bad"), CustomerID: accessPtr(99)}, nil); !errors.Is(err, access.ErrWriteConflict) {
		t.Fatal(err)
	}
	if _, err := UpdateModelInputWithConn(ctx, conn, &accessOrderKey{}, input, nil); !errors.Is(err, modelbind.ErrMissingKey) {
		t.Fatal(err)
	}
	if _, err := UpdateModelInputWithConn(ctx, conn, key, accessDecode(t, `{}`), nil); err == nil {
		t.Fatal("empty update accepted")
	}
	if len(conn.queries) != 1 {
		t.Fatal("invalid update executed")
	}
	conn.affected = 0
	if _, err := UpdateModelInputWithConn(ctx, conn, key, input, nil); !errors.Is(err, ds.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestAccessDeleteGroupsAllCompositeKeys(t *testing.T) {
	ctx := accessTestContext(t)
	keys := []types.DBModel{
		&accessCompositeKey{DocumentID: accessPtr(1), LineNo: accessPtr(2)},
		&accessCompositeKey{DocumentID: accessPtr(3), LineNo: accessPtr(4)},
	}
	query, params, protected, err := buildAccessDeleteModelQuery(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	if !protected || !strings.HasSuffix(query, ") AND (customer_id = $5)") || !strings.Contains(query, " OR ") {
		t.Fatal(query)
	}
	if !reflect.DeepEqual(params, []any{int64(1), int64(2), int64(3), int64(4), int64(42)}) {
		t.Fatal(params)
	}
	conn := &accessCaptureConn{affected: 1}
	count, err := DeleteModelWithConn(ctx, conn, keys, nil)
	if err != nil || count != 1 {
		t.Fatalf("partial authorized delete count=%d err=%v", count, err)
	}
	if _, _, _, err := buildAccessDeleteModelQuery(ctx, []types.DBModel{&accessCompositeKey{DocumentID: accessPtr(1)}}); !errors.Is(err, modelbind.ErrMissingKey) {
		t.Fatal(err)
	}
	if _, _, _, err := buildAccessDeleteModelQuery(ctx, []types.DBModel{&accessOrderKey{ID: accessPtr(1)}, &accessUnmarkedOrder{ID: 2}}); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
}

func TestAccessReadScopesDetailCollectionAndTotals(t *testing.T) {
	ctx := accessTestContext(t)
	detail, _, err := prepareDetailModelSelect(ctx, &accessOrderKey{ID: accessPtr(10)}, &accessOrder{})
	if err != nil {
		t.Fatal(err)
	}
	params := []any{}
	query, err := detail.BuildSQL(&params)
	if err != nil || !strings.Contains(query, "WHERE (id = $1) AND (customer_id = $2)") {
		t.Fatalf("%s %v", query, err)
	}
	if _, _, err := prepareDetailModelSelect(ctx, &accessOrderKey{}, &accessOrder{}); !errors.Is(err, modelbind.ErrMissingKey) {
		t.Fatal(err)
	}
	collection, err := prepareCollectionModelSelect(ctx, &accessOrderList{}, modelbind.CollectionParams{Filter: []modelbind.CollectionFilter{
		{Join: modelbind.FilterParJoinAnd, Fields: map[string]modelbind.CollectionFilterField{"name": {Operator: modelbind.FilterOperParEq, Value: "one"}}},
		{Join: modelbind.FilterParJoinOr, Fields: map[string]modelbind.CollectionFilterField{"name": {Operator: modelbind.FilterOperParEq, Value: "two"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	params = []any{}
	query, totals, err := collection.BuildCollectionSQL(&params)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{query, totals} {
		if !strings.Contains(sql, "WHERE ((name = $1) OR (name = $2)) AND (o.customer_id = $3)") {
			t.Fatal(sql)
		}
	}
	if len(params) != 3 {
		t.Fatal(params)
	}
}

func TestAccessEmptyMembershipIsFalse(t *testing.T) {
	r := accessTestRegistry(t)
	if err := r.Register("Order", access.Read, func(context.Context, Principal) (access.Policy, error) {
		return access.Scope(access.In("customer_id")), nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithPrincipal(ContextWithAccess(context.Background(), &AccessController{Registry: r}), Principal{UserID: "7"})
	builder, err := prepareCollectionModelSelect(ctx, &accessOrderList{}, modelbind.CollectionParams{})
	if err != nil {
		t.Fatal(err)
	}
	params := []any{}
	query, totals, err := builder.BuildCollectionSQL(&params)
	if err != nil || !strings.Contains(query, "WHERE (FALSE)") || !strings.Contains(totals, "WHERE (FALSE)") {
		t.Fatalf("%s %s %v", query, totals, err)
	}
}

func TestAccessWriteMappingsAndDefaultSemantics(t *testing.T) {
	r := accessTestRegistry(t)
	if err := r.Register("Order", access.Insert, func(context.Context, Principal) (access.Policy, error) {
		return access.ForInsert(access.WriteRules{"customer_id": access.Default(42)}), nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithPrincipal(ContextWithAccess(context.Background(), &AccessController{Registry: r}), Principal{UserID: "7"})
	got, err := PrepareAccessInput(ctx, accessDecode(t, `{"name":"test","customer_id":99}`), access.Insert)
	if err != nil || *got.Model.CustomerID != 99 {
		t.Fatalf("default forced submitted value: %v", err)
	}
	if _, err := PrepareAccessInput(ctx, accessDecode(t, `{"name":"test","customer_id":null}`), access.Insert); err == nil {
		t.Fatal("default replaced explicit NULL on required field")
	}
	if _, err := IntersectAccessPolicies(access.Update, access.Policy{Decision: access.Restricted, Rows: access.True(), Writes: access.WriteRules{"customer_id": access.Immutable()}}); err != nil {
		t.Fatal(err)
	}
	bad := &AccessController{Registry: NewPolicyRegistry()}
	if err := bad.Registry.RegisterModel(accessOrder{}, modelpg.PolicyBinding{}); err != nil {
		t.Fatal(err)
	}
	if err := bad.Registry.Register("Order", access.Read, func(context.Context, Principal) (access.Policy, error) {
		return access.Scope(access.Eq("customer_id", 42)), nil
	}); err != nil {
		t.Fatal(err)
	}
	badCtx := ContextWithPrincipal(ContextWithAccess(context.Background(), bad), Principal{UserID: "7"})
	if _, _, err := prepareDetailModelSelect(badCtx, &accessOrderKey{ID: accessPtr(1)}, &accessOrder{}); !errors.Is(err, access.ErrInvalidField) {
		t.Fatal(err)
	}
}

func TestAccessNoResultIsNotSuccessfulInsert(t *testing.T) {
	conn := &accessCaptureConn{affected: 0}
	_, err := InsertModelInputWithConn(accessTestContext(t), conn, accessDecode(t, `{"name":"new"}`), nil)
	if !errors.Is(err, access.ErrDenied) {
		t.Fatal(err)
	}
}

func TestAccessPrincipalInjectionRequiresPolicies(t *testing.T) {
	ctx := ContextWithPrincipal(accessTestContext(t), Principal{UserID: "admin"})
	if _, err := ResolveModelAccess(ctx, &accessOrder{}, access.Read); !errors.Is(err, access.ErrDenied) {
		t.Fatal(err)
	}
	principal := Principal{UserID: "7", Roles: []string{"customer"}, Claims: map[string]any{"customer_id": 42}}
	ctx = ContextWithPrincipal(accessTestContext(t), principal)
	principal.Roles[0] = "admin"
	principal.Claims["customer_id"] = 99
	resolved, err := ResolveModelAccess(ctx, &accessOrder{}, access.Read)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(resolved.Policy.Rows.Values()) != "[42]" {
		t.Fatal(resolved.Policy.Rows.Values())
	}
}

func TestAccessReadDecisionCannotAuthorizeWriteBuilder(t *testing.T) {
	resolved, err := ResolveModelAccess(accessTestContext(t), &accessOrder{}, access.Read)
	if err != nil {
		t.Fatal(err)
	}
	builder := modelpg.NewPgUpdate(&accessOrder{})
	if err := resolved.ApplyTo(builder); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
	builder.AddField("name", "changed")
	builder.Filter().Add("", "id", 1, types.SQLFilterOperatorEq, types.SQLFilterJoinAnd)
	params := []any{}
	if _, err := builder.BuildSQL(&params); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("ignored installation error did not fail closed: %v", err)
	}
}

func TestAccessLogicalWriteMappingAndImmutableRule(t *testing.T) {
	registry := NewPolicyRegistry()
	if err := registry.RegisterModel(accessOrder{}, modelpg.PolicyBinding{
		Rows:   modelpg.FieldMap{"owner": "customer_id"},
		Writes: modelpg.FieldMap{"owner": "customer_id"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("Order", access.Insert, func(context.Context, Principal) (access.Policy, error) {
		return access.ForInsert(access.WriteRules{"owner": access.Enforce(42)}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("Order", access.Update, func(context.Context, Principal) (access.Policy, error) {
		return access.Policy{Decision: access.Restricted, Rows: access.Eq("owner", 42), Writes: access.WriteRules{"owner": access.Immutable()}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithPrincipal(ContextWithAccess(context.Background(), &AccessController{Registry: registry}), Principal{UserID: "7"})
	effective, err := PrepareAccessInput(ctx, accessDecode(t, `{"name":"mapped"}`), access.Insert)
	if err != nil || effective.Model.CustomerID == nil || *effective.Model.CustomerID != 42 {
		t.Fatalf("%#v %v", effective, err)
	}
	conn := &accessCaptureConn{affected: 1}
	if _, err := UpdateModelInputWithConn(ctx, conn, &accessOrderKey{ID: accessPtr(1)}, accessDecode(t, `{"customer_id":42}`), nil); !errors.Is(err, access.ErrWriteConflict) {
		t.Fatal(err)
	}
	if _, err := UpdateModelInputWithConn(ctx, conn, &accessOrderKey{ID: accessPtr(1)}, accessDecode(t, `{"name":"allowed"}`), nil); err != nil {
		t.Fatal(err)
	}
}

type accessBadReceiver struct {
	ID int `json:"id"`
}

func (accessBadReceiver) Relation() string        { return "bad_receiver" }
func (*accessBadReceiver) AccessResource() string { return "BadReceiver" }

func TestAccessRegistrationRejectsBypassableMarkersAndUnsafeMappings(t *testing.T) {
	registry := NewPolicyRegistry()
	if err := registry.RegisterModel(&accessBadReceiver{}, modelpg.PolicyBinding{}); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
	if err := registry.RegisterModel(accessUnmarkedOrder{}, modelpg.PolicyBinding{}); !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
	if err := registry.RegisterModel(accessOrder{}, modelpg.PolicyBinding{Rows: modelpg.FieldMap{"owner": "customer_id) OR TRUE --"}}); err == nil {
		t.Fatal("unsafe row mapping accepted")
	}
	if err := registry.RegisterModel(accessOrder{}, modelpg.PolicyBinding{Writes: modelpg.FieldMap{"owner": "customer_id", "customer_id": "customer_id"}}); err == nil {
		t.Fatal("ambiguous write mapping accepted")
	}
}
