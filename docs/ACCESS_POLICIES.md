# Automatic row and write policies

This integration requires the policy-enabled `modelbind` delivered with
`modelbind-access-policies.zip`. It does not work with the older modelbind API.
`webapp` resolves trusted identity, application/role/user presets and concrete
model bindings. `modelbind` builds grouped, parameterized SQL and enforces write
rules. Authentication and route/method permissions remain application concerns.

## Compatibility and activation

Existing unmarked models keep their legacy CRUD behavior. Protection is opt-in:

1. Add `AccessResource() string` to **every** representation of a protected
   resource: writable model, list/detail projections and delete-key models.
2. Register each concrete model and its explicit SQL field mappings.
3. Register a policy factory for each supported operation, globally or for the
   relevant roles/users.
4. Set `AppContext.Access` and supply a trusted principal resolver.

A marked model without an access context, model binding, principal or applicable
policy never becomes unrestricted. There is no administrative bypass flag. Use an
explicit principal and explicit `access.AllowAll()` policy for privileged jobs.

No generated application files or codegen templates are changed by this patch.
Markers can be placed in a handwritten Go file next to generated model types:

```go
func (Order) AccessResource() string {
	return "Order"
}

func (OrderList) AccessResource() string {
	return "Order"
}

func (OrderDetail) AccessResource() string {
	return "Order"
}

func (OrderKey) AccessResource() string {
	return "Order"
}
```

When `Relation` has a value receiver, `AccessResource` must also have a value
receiver. `RegisterModel` checks this to prevent value-form delete keys from
losing a pointer-only marker.

A registry also rejects an unmarked model using an already registered relation
when an access context is available. This is a guard against accidental omissions,
not a substitute for marking every representation. Arbitrary new unmarked models
or raw SQL cannot be made protected by inference.

## Principal resolution

```go
type Principal struct {
	UserID string
	Roles []string
	Claims map[string]any
}

type PrincipalResolver func(
	context.Context,
	session.Session,
) (Principal, error)
```

The resolver must authenticate the session/token, check that it is still valid,
and load current user/role information from trusted storage. This library does
not guess session keys such as `user_id`, `role_id`, `customer_id` or their types.
It does not trust values submitted in an HTTP body, query, header or WebSocket
payload to establish a principal.

`UserID` is the application user's stable identifier, not the session ID. An empty
ID is anonymous and cannot access a protected model. Role names are exact,
case-sensitive strings. Duplicate roles are removed. Claims are independent scalar
snapshots normalized by `modelbind/access`: for example, signed Go integers become
`int64`. Arrays and maps are not scalar claims; resolve assigned-site/customer sets
inside a policy factory. No policy state is serialized into the session.

Attach your resolver and registry to the application:

```go
controller := &webapp.AccessController{
	Registry: registry,
	ResolvePrincipal: resolveAuthenticatedPrincipal,
}

appContext.Access = controller
```

Here `resolveAuthenticatedPrincipal` is your application function with the
`webapp.PrincipalResolver` signature. It must recheck validity rather than blindly
return a login-time snapshot, particularly for long-lived WebSocket sessions.

## Model bindings

Bind each representation explicitly. A policy's row columns need not be returned
in the projection or present in the writable Go struct.

```go
binding := pg.PolicyBinding{
	Rows: pg.FieldMap{
		"customer_id": "customer_id",
	},
	Writes: pg.FieldMap{
		"customer_id": "customer_id",
	},
}

if err := registry.RegisterModel(models.Order{}, binding); err != nil {
	return err
}

if err := registry.RegisterModel(models.OrderKey{}, pg.PolicyBinding{
	Rows: binding.Rows,
}); err != nil {
	return err
}
```

Register list and detail models too, using their actual query column references.
For a relation `orders AS o`, the row mapping can be
`"customer_id": "o.customer_id"`. Do not use that alias for a view or base-table
relation that does not declare it. The registry snapshots the exact `Relation()`
string and rejects a runtime model whose resource or relation changes.

Write targets must be unqualified columns. When the field is in the DTO, its target
column must match the modelbind metadata field ID (normally its JSON tag). Logical
policy IDs may differ: `"owner": "customer_id"` is supported. This mapping is used
for typed preparation as well as SQL enforcement. A column absent from the DTO
remains governed at SQL-build time, rather than silently dropping the rule.

All mappings are copied and validated at registration. Duplicate model
registrations and ambiguous write-column mappings are errors. Configure at startup;
do not mutate exported `AccessController` fields while requests are running.

## Application, role and user presets

Register a separate policy for each operation: `Read`, `Insert`, `Update`, `Delete`.
Read is shared by detail, list and totals. There is no fallback from Update to Read.
An intentionally forbidden operation can have an explicit `access.DenyAll()` factory.

```go
ownOrders := func(ctx context.Context, principal webapp.Principal) (access.Policy, error) {
	customerID, ok := principal.Claims["customer_id"].(int64)
	if !ok || customerID <= 0 {
		return access.DenyAll(), nil
	}
	return access.Scope(access.Eq("customer_id", customerID)), nil
}

if err := registry.RegisterRole("customer", "Order", access.Read, ownOrders); err != nil {
	return err
}

if err := registry.RegisterRole("customer", "Order", access.Delete, ownOrders); err != nil {
	return err
}

if err := registry.RegisterRole("customer", "Order", access.Insert,
	func(ctx context.Context, principal webapp.Principal) (access.Policy, error) {
		customerID, ok := principal.Claims["customer_id"].(int64)
		if !ok || customerID <= 0 {
			return access.DenyAll(), nil
		}
		return access.ForInsert(access.WriteRules{
			"customer_id": access.Enforce(customerID),
		}), nil
	},
); err != nil {
	return err
}

if err := registry.RegisterRole("customer", "Order", access.Update,
	func(ctx context.Context, principal webapp.Principal) (access.Policy, error) {
		customerID, ok := principal.Claims["customer_id"].(int64)
		if !ok || customerID <= 0 {
			return access.DenyAll(), nil
		}
		return access.Policy{
			Decision: access.Restricted,
			Rows: access.Eq("customer_id", customerID),
			Writes: access.WriteRules{
				"customer_id": access.Enforce(customerID),
			},
		}, nil
	},
); err != nil {
	return err
}
```

Use `Register(resource, operation, factory)` for a restriction applying to every
principal, and `RegisterUser(userID, resource, operation, factory)` for a
user-specific preset. Application, matching-role and matching-user row restrictions
are combined with **AND**, not last-writer-wins. A global preset applies even when
no role-specific preset matches; row presets are not the route permission system.
No matching preset at all is denied. `AllowAll` does not override another matching
restriction. Express alternatives explicitly inside one `access.Or(...)` policy.

Identical write rules for one field can be repeated. Different rules for the same
field are rejected as a configuration error, not silently replaced. For example,
two independently computed Allowed sets must be intersected explicitly in one
factory; this version does not synthesize arbitrary write-rule intersections.
`IntersectAccessPolicies` exposes this same composition for custom factories.

Factories run without the registry lock. They must not recursively resolve their
own resource/operation; use trusted identity/assignment lookup code for their data.
A request caches both policy results and errors. Registering a later preset does
not change an already resolved operation in that request; new request scopes see
new registrations.

## Binding and validation order

The standard body/request write binders (`RequestInputBinder`, `InsertInputBinder`,
`UpdateInputBinder` and their model-only counterparts) now perform:

```text
decode typed values + original absence
resolve trusted principal and operation policy
validate mappings and enforce submitted-value restrictions
construct effective input with server-supplied values
validate required fields and ordinary field rules
call the service
```

This allows an omitted required `customer_id` to be supplied by an enforced insert
rule. A conflicting value or explicit NULL is rejected, not overwritten.
`Default` only supplies an absent insert field; it is not an authorization rule.
`Enforce` leaves an absent update field unchanged while rejecting a conflicting
submitted update. `Allowed` does not choose a default. `Immutable` rejects any
submitted update of that field, even a value equal to the current database value.

`PrepareAccessInput(ctx, input, operation)` exposes this sequence for custom
binders. Query/path/key binders retain their original validation: they must not
mistake a detail key for an update body. Custom write routes using these binders
can call `PrepareAccessInput` explicitly; CRUD helpers still prepare/enforce values
before their own validation. `JSONInputBinder` retains its decode-only contract.

Effective input is a shallow model copy with independent absence metadata.
Unchanged nested reference-valued fields remain shared; do not mutate them.
Rules can supply plain application-written columns, not `srvCalc` fields, which
modelbind treats as database-generated RETURNING fields.

Use `ModelInput` variants for PATCH. Full-model variants have no original absence
metadata: nil/zero fields are supplied values, not inferred omissions. A nil owner
in a full model is therefore not treated as an absent enforced owner. For protected
writes use returned maps for server-generated fields, or prepare and retain the
effective input before calling CRUD; do not rely on mutation of the original
submitted model by RETURNING.

## Automatic CRUD enforcement

All provider and `WithConn` variants of InsertModel[Input], UpdateModel[Input],
DeleteModel, FetchModel and FetchCollectionModel are covered. Provider wrappers
resolve identity/policy before acquiring the main CRUD connection. WithConn calls
also resolve/enforce independently. When a resolver needs its own connection,
resolve it before opening a constrained-pool transaction to avoid nested
acquisition; no database connection is implicitly passed to factories.

Server predicates are independent of client filters:

```sql
WHERE
	(request_condition_1 OR request_condition_2)
	AND (customer_id = $3)
```

Details are scoped as well as lists. Totals reuse the scoped collection filter.
Complete caller keys are required independently of server predicates for protected
updates/details/deletes. Numeric zero keys are not assumed invalid: validate
application-specific key ranges separately.

A batch delete forms `(key_1 OR key_2 OR ...) AND (server_scope)` and rejects mixed
protected/unprotected models or different protected key types. It preserves the
helper's affected-row-count semantics: **permitted matching rows are deleted even
when other requested rows are absent or inaccessible**. It does not provide
all-or-nothing authorization for the batch. Use an application-owned transaction
and appropriate concurrency checks for stricter document operations.

Protected detail/update/delete misses return a 404 `not_found`, preserving
`errors.Is(err, ds.ErrNoRows)`. The response does not distinguish inaccessible from
nonexistent records. A protected insert returning no row / affecting zero rows is
an error rather than a false success. Underlying database exceptions retain the
existing database-error normalization.

All builders use error-returning SQL construction. Policy errors occur before SQL
execution. An explicit `AllowAll` still uses protected-path key/error safeguards.

## HTTP, WebSocket, direct calls and jobs

`BuildHandler` installs the access scope before the HTTP binder, including custom
handlers. `CallServiceMethod` propagates session, query ID and the configured
controller into the context passed to the service method. The binder and CRUD
share one identity/policy snapshot per request.

WebSocket dispatch starts a new authorization scope for each message, clears any
explicit identity inherited from the HTTP upgrade when setting the message session,
and resolves the principal again. PostgreSQL local-event dispatch also starts a
new scope. Neither has an implicit privileged identity.

For JSON transports, register typed decoders for methods taking protected
`ModelInput` or `UpdateByKeysInput`. The old plain `json.Unmarshal` fallback cannot
reconstruct which model fields were absent and is rejected for those protected
input types:

```go
webapp.MustRegisterService(
	"Order",
	&OrderService{},
	NewOrderService,
	webapp.WithMethodInputDecoder("Create", webapp.ModelInputJSONDecoder[*models.Order](true)),
	webapp.WithMethodInputDecoder("Update", webapp.UpdateByKeysJSONDecoder[*models.OrderKey, *models.Order]()),
)
```

The corresponding payloads are `{"name":"new"}` for Create and
`{"keys":{"id":10},"input":{"name":"changed"}}` for Update. The browser must not
serialize `ModelInput` or send presence metadata. `ModelJSONDecoder` is available
for full-model methods. Decoder names must match registered methods.

`CallServiceJSON` and `DecodeInputForServiceContext` are shared entry points for
context-aware transport decoding. `DecodeInputForService` remains available, but
has no authenticated context and cannot decode an authorized protected write by
itself. Standard HTTP binders are independent of these JSON decoder options.

Direct calls and jobs must deliberately attach configuration and trusted identity:

```go
ctx := webapp.ContextWithAccess(context.Background(), controller)
ctx = webapp.ContextWithPrincipal(ctx, webapp.Principal{
	UserID: "nightly-import",
	Roles: []string{"importer"},
})
```

This identity still needs applicable registered policies. Do not reuse an access
context across users or unrelated jobs. `ContextWithSession` invalidates both
cached and explicitly supplied identities; changing actor starts a new scope.
`PrincipalFromContext` returns a copy of the currently resolved principal.

## Errors and notifications

Authorization errors are normalized for HTTP and WebSocket responses. Missing
identity gives 401; no matching preset/explicit denial gives 403; write conflicts
give 403 with a field/reason code but no policy value. Missing configuration,
invalid mappings and failed policy lookups give a sanitized 500. Missing caller
keys and empty updates give 400. Ordinary model validation remains 400.

The existing `WithCRUDNotifications` mechanism includes input and result values.
After a protected operation, ordinary `EventPublisher` broadcasting is therefore
suppressed. To deliver those events, provide an `EventPublisher` that also
implements:

```go
type AccessEventPublisher interface {
	PublishAccessEvent(
		context.Context,
		webapp.Principal,
		string,
		webapp.ServiceMethodEvent,
	) error
}
```

That implementation must authorize recipients and redact/scope payloads. The
interface is an integration hook, not automatic recipient authorization. The
built-in WebSocket broadcaster does not implement it. Events after unprotected
operations retain legacy delivery. Explicit calls to Notify/PublishEvent and
externally received database notifications remain application responsibilities.

## Custom SQL and limits

`ResolveModelAccess` plus `ResolvedAccess.ApplyTo` can scope custom modelbind
PostgreSQL builders. The operation must match the builder; a mismatch poisons the
builder with a denied policy even when the caller ignores the returned error.
The exported helper supports the standard modelbind PostgreSQL builders.

Policies do not intercept arbitrary `conn.Query`/`Exec`, raw reports, exports,
separate relationship lookups or custom SQL functions. Outer-row filtering cannot
secure a projection that has already aggregated protected data inside an unscoped
subquery. Trigger-modified final values, cross-table write validation, database
roles and native PostgreSQL RLS are outside this implementation.

## Dependency setup

The uploaded go.mod's Go version, pinned requirements and local replace paths are
unchanged. No unpublished modelbind tag or commit hash has been invented. The
replacement at `/home/andrey/go/modelbind` must contain the earlier policy patch.
Consuming applications must also resolve that patched checkout (for example through
their own replace/workspace), or a real published revision containing it. Before
publishing webapp without local replacements, update its modelbind requirement to
that real revision.

See `IMPLEMENTATION_NOTES.md` for the exact validation environment and limits.
