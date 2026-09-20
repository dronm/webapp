package webapp_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	"github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
	"github.com/dronm/webapp"
)

type exampleScopedOrder struct {
	CustomerID *int64  `json:"customer_id" required:""`
	Name       *string `json:"name" required:""`
}

func (exampleScopedOrder) Relation() string       { return "orders" }
func (exampleScopedOrder) AccessResource() string { return "Order" }

func exampleScope() context.Context {
	registry := webapp.NewPolicyRegistry()
	if err := registry.RegisterModel(exampleScopedOrder{}, pg.PolicyBinding{
		Rows:   pg.FieldMap{"customer_id": "customer_id"},
		Writes: pg.FieldMap{"customer_id": "customer_id"},
	}); err != nil {
		panic(err)
	}
	if err := registry.RegisterRole("customer", "Order", access.Read,
		func(_ context.Context, p webapp.Principal) (access.Policy, error) {
			return access.Scope(access.Eq("customer_id", p.Claims["customer_id"])), nil
		},
	); err != nil {
		panic(err)
	}
	if err := registry.RegisterRole("customer", "Order", access.Insert,
		func(_ context.Context, p webapp.Principal) (access.Policy, error) {
			return access.ForInsert(access.WriteRules{"customer_id": access.Enforce(p.Claims["customer_id"])}), nil
		},
	); err != nil {
		panic(err)
	}
	ctx := webapp.ContextWithAccess(context.Background(), &webapp.AccessController{Registry: registry})
	// This explicit identity represents already authenticated trusted server
	// code. An HTTP application normally supplies a PrincipalResolver instead.
	return webapp.ContextWithPrincipal(ctx, webapp.Principal{
		UserID: "7", Roles: []string{"customer"}, Claims: map[string]any{"customer_id": int64(42)},
	})
}

func ExamplePrepareAccessInput() {
	input, err := modelbind.DecodeJSONBodyInput[*exampleScopedOrder](strings.NewReader(`{"name":"New order"}`))
	if err != nil {
		panic(err)
	}
	effective, err := webapp.PrepareAccessInput(exampleScope(), input, access.Insert)
	if err != nil {
		panic(err)
	}
	fmt.Println(*effective.Model.CustomerID)
	fmt.Println(effective.IsPresent("customer_id"))
	fmt.Println(input.Model.CustomerID == nil)
	// Output:
	// 42
	// true
	// true
}

func ExampleResolveModelAccess() {
	model := &exampleScopedOrder{}
	resolved, err := webapp.ResolveModelAccess(exampleScope(), model, access.Read)
	if err != nil {
		panic(err)
	}
	builder := pg.NewPgDetailSelect(model, &pg.PgFilters{})
	builder.AddField("name", &model.Name)
	builder.Filter().Add("", "id", 10, types.SQLFilterOperatorEq, types.SQLFilterJoinAnd)
	if err := resolved.ApplyTo(builder); err != nil {
		panic(err)
	}
	params := []any{}
	query, err := builder.BuildSQL(&params)
	if err != nil {
		panic(err)
	}
	fmt.Println(query)
	fmt.Println(params)
	// Output:
	// SELECT name FROM orders WHERE (id = $1) AND (customer_id = $2)
	// [10 42]
}
