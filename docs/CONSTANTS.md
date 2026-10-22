# Application constants

`github.com/dronm/webapp/constants` manages application configuration that an
administrator can change while the application is running. It supplies a common
PostgreSQL table, typed project definitions, a shared cache, batch reads and
writes, HTTP/WebSocket services, and synchronization between application servers.

The package does not create tables or seed values automatically. Apply migrations
before starting the application.

## Components and ownership

| Component | Owner | Responsibility |
| --- | --- | --- |
| `public.webapp_constants` | Common migration | Metadata, current JSONB values, versions |
| `Registry` | Project startup | Logical types, Go types, validators, editor hints |
| `PostgresRepository` | Common package | Reads from the primary and atomic writes |
| `Manager` | One per process/database | Shared cache, lifecycle, reconciliation, hooks |
| `Constant[T]` | Project services | Concrete Go reads and writes using that manager |
| `Service` and `Routes` | Common package | Administration API with mandatory authorization |

Use a stable `code` in project code. Numeric `id` values are convenient for
collection selection and transport requests, but can differ between databases.
IDs and codes are immutable after insertion. Constants have no generic
create/delete API; introduce or remove definitions through migrations.
Keep generated IDs unique over the catalogue's lifetime rather than reusing
deleted IDs. Code-based accessors verify the requested code across refreshes and
writes, including a delete/reseed that assigns the same code a different ID.

The registry is copied when `NewManager` runs. Finish registration first; later
changes to the registry do not change an existing manager.

## Database migrations and initial values

Copy [001_constants.up.sql](../constants/sql/001_constants.up.sql) into your
project's migration sequence. Its SQL is also available as
`constants.MigrationUp` for migration runners that execute Go-provided strings.
The corresponding rollback is `constants.MigrationDown` or
[001_constants.down.sql](../constants/sql/001_constants.down.sql). Rolling back
the common migration deletes all constant values.

The fixed table has these columns:

| Column | Purpose |
| --- | --- |
| `id` | Generated bigint identity |
| `code` | Unique fixed project identifier, at most 256 UTF-8 bytes |
| `name` | Localized display name |
| `description` | Administrator help text |
| `value_type` | Logical type from the supported list below |
| `value` | Current JSONB value |
| `version` | Positive row-change counter managed by a trigger |
| `updated_at` | Database timestamp of the last effective change |

Project migrations seed rows after the common migration. For example:

```sql
INSERT INTO public.webapp_constants (code, name, description, value_type, value)
VALUES (
	'default_price_type',
	'Вид цены по умолчанию',
	'Вид цены для нового заказа.',
	'json',
	'{"id":1,"name":"Розничная"}'::jsonb
)
ON CONFLICT (code) DO NOTHING;
```

`DO NOTHING` preserves a value already edited by an administrator. Do not reset
current values from defaults at each startup. The full project seed is in
[examples/constants/seed.sql](../examples/constants/seed.sql).

Names, descriptions, types, and structural changes belong to project migrations.
A type/structure migration must remain readable by every server version running
during a rolling deployment. The row `version` is a concurrency counter, not a
JSON schema version.

The database checks the basic JSON kind and supported scalar ranges. It cannot
execute a project's Go validator. Normal writes should go through `Manager` or
`Constant[T]`; direct SQL changes must satisfy project invariants themselves.

## Value types and validation

| Logical type | JSON representation | Typical Go type |
| --- | --- | --- |
| `TypeBool` | `true` or `false` | `bool` |
| `TypeInt64` | Integer number | `int64` or a checked integer alias |
| `TypeFloat64` | Finite number | `float64` |
| `TypeString` | String | `string` |
| `TypeDecimal` | Exact decimal string, e.g. `"1234.50"` | `string` or a compatible custom type |
| `TypeJSON` | Object, array, or other JSON value | Project struct, concrete map/slice, `json.RawMessage` |

Decimal strings use fixed notation with an optional minus sign and fractional
part. Exponents and leading plus signs are not accepted. Use `TypeDecimal` for
precision-sensitive decimal configuration; `TypeFloat64` has ordinary binary
floating-point semantics.

Registered Go types must match the logical type. `TypeJSON` uses concrete types
rather than decoding numbers into `map[string]any` or `any`; use a struct,
concretely typed collection, or `json.RawMessage` when the JSON structure is
intentionally open.

The decoder rejects unknown struct fields and trailing JSON. Custom JSON
unmarshalers remain responsible for their own decoding rules. The common layer
validates the logical kind and representable range, then runs the project
validator. Both generic `Set` and typed `SetValue` use this path. A missing JSON
value, a JSON `null`, and a Go zero value are distinct. JSON `null` requires an
explicit `AllowNull` and a Go type that can represent null.

Ordinary Go struct decoding maps an absent scalar field to its zero value.
Validate required fields explicitly, or use pointer fields when absence must be
distinguished. Validators run on database loads as well as writes and should be
side-effect free.

Define project types and register them before constructing the manager:

```go
type DefaultPriceType struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

registry := constants.NewRegistry()
err := constants.Register(registry, constants.Definition[DefaultPriceType]{
	Code: "default_price_type", ValueType: constants.TypeJSON, Required: true,
	Validate: func(ctx context.Context, value DefaultPriceType) error {
		if value.ID <= 0 || strings.TrimSpace(value.Name) == "" {
			return errors.New("a positive ID and a name are required")
		}
		return nil
	},
})
if err != nil {
	return err
}
```

`Required: true` makes startup fail when the row is absent. Stored values and
types are validated during startup and refresh. A missing optional definition
still returns `ErrNotFound` when read; it does not silently return a zero value.

Validators can check references through a project repository. They should not
call back into the same constants manager. `Editor` can contain project-specific
JSON hints for an administrator UI; these are not executable validation rules.
Unregistered database rows can be read through the generic API after basic
logical-type checks, but writes require a matching registered definition.

## Process startup and shutdown

The compiled [project example](../examples/constants/project.go) registers a
boolean, an integer with limits, and a structured price-type constant. It accepts
the project's existing `ds.Provider` and a primary PostgreSQL connection string.

The essential wiring is:

```go
repository, err := constants.NewPostgresRepository(db)
if err != nil {
	return err
}
listener, err := constants.NewPGListener(primaryDSN)
if err != nil {
	return err
}
manager, err := constants.NewManager(constants.Config{
	Repository: repository,
	Registry: registry,
	Notifications: listener,
	ReconcileInterval: time.Minute,
	OnError: func(err error) {
		slog.Error("constants synchronization", "error", err)
	},
})
if err != nil {
	return err
}
if err := manager.Start(appLifetimeContext); err != nil {
	return err
}
```

Create the manager once at application startup and share it with all request
services. `webapp.ServiceFactory` creates request-scoped services; the factory
must capture the existing manager instead of creating another one.

The repository and listener must target the same primary database. The listener
holds its own persistent PostgreSQL session. Connect it directly or through a
session-pooling connection; PgBouncer transaction pooling does not preserve
`LISTEN` registrations. The package's listener is independent of `events.Server`
and does not require browser event subscriptions or a connected WebSocket client.

`Start` establishes and commits `LISTEN`, then validates and loads the initial
catalogue. It returns an error if initial synchronization fails. Pass the
application's lifetime context; do not cancel it immediately after startup.

During shutdown, stop accepting application work and close the manager with a
bounded context before closing the database provider:

```go
closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := manager.Close(closeCtx); err != nil {
	slog.Error("close constants", "error", err)
}
```

The manager owns its notification subscription and background goroutines. It
does not own or close the supplied repository/database provider.

Configuration defaults are:

| Option | Default | Behavior |
| --- | --- | --- |
| `ReconcileInterval` | One minute | Background full-catalogue reconciliation |
| `RefreshTimeout` | Ten seconds | Startup, listener connection, and background refresh timeout |
| `CallbackTimeout` | Ten seconds | Context deadline supplied to a project hook |
| `CallbackRetryInterval` | One second | Initial delay for retrying a failed current-state hook; at most one minute |
| `MaxStale` | Five minutes | Maximum age of the last full successful reconciliation before reads force a refresh |

Zero selects a default; negative durations are rejected. A nil `Notifications`
source enables polling-only operation. Set `OnError` to observe background
failures; its callback must return promptly. Callers' read/write contexts control
their synchronous operations, so use bounded request contexts.
`CallbackRetryInterval` cannot exceed one minute. Retry delays grow exponentially
from that interval and are capped at one minute.

## Concrete Go accessors

```go
priceType, err := constants.NewConstant[DefaultPriceType](manager, "default_price_type")
if err != nil {
	return err
}
current, err := priceType.GetValue(ctx)
if err != nil {
	return err
}
// current is DefaultPriceType, with compile-time field/type checks.
fmt.Println(current.ID, current.Name)
```

`NewConstant[T]` verifies that `T` is exactly the Go type registered for that
code. It uses the same cache and write path as the generic API. Typed reads
decode a fresh value, so changing a returned map, slice, or pointer cannot mutate
another caller's cached state.

`GetVersioned(ctx)` returns `(T, int64, error)`. Use its version for conditional
writes:

```go
current, version, err := priceType.GetVersioned(ctx)
if err != nil {
	return err
}
current.Name = "Оптовая"
if err := priceType.SetValueIfVersion(ctx, current, version); err != nil {
	if errors.Is(err, constants.ErrConflict) {
		// Reload and let the caller review the concurrent change.
	}
	return err
}
```

`SetValue(ctx, value)` also validates and persists to PostgreSQL. A concurrent
change between validation and writing can still return `ErrConflict`.

The project example exposes `NewConstantDefaultPriceTypeService(manager)` and
its `GetValue`/`SetValue` methods for projects that prefer a named service wrapper.

## Administration service and authorization

```go
err := constants.RegisterService("Constant", manager, authorizeConstants)
if err != nil {
	return err
}
routes, err := constants.Routes("/constants", "Constant")
if err != nil {
	return err
}
handler := webapp.MustBuildRouter(appCtx, routes, yourSessionMiddleware)
```

Provide `authorizeConstants` with this signature:

```go
func authorizeConstants(ctx context.Context, operation constants.Operation, ids []int64) error
```

It runs inside the service before manager access for both HTTP and WebSocket
calls. A nil authorizer is rejected at construction. `OperationList` receives
nil IDs and requires permission to inspect the whole catalogue's metadata.
`OperationDetail`, `OperationGet`, and `OperationSet` receive all requested IDs;
deny the whole request if any ID is not permitted.

The compiled example's `AuthorizeAdmin` uses `webapp.PrincipalFromContext` and
requires the `admin` role. Supply your existing authenticated
`AppContext.Access.ResolvePrincipal` and session middleware. Route permissions
may provide an additional check, but do not replace the service authorizer;
otherwise a WebSocket call could reach a service without the HTTP route check.

The manager and typed accessors are trusted server APIs. They do not make
user-specific authorization decisions. Put them behind appropriately authorized
project services when used on behalf of an end user.

The constants package does not broadcast values to browser subscribers. Any
browser notification should be a separate project event with its own recipient
authorization.

## HTTP requests

| HTTP request | Service call | Result |
| --- | --- | --- |
| `GET /constants` | `Constant.List` | Metadata collection and total |
| `GET /constants/{id}` | `Constant.Detail` | One metadata object |
| `POST /constants/get` | `Constant.Get` | Array of values and versions |
| `POST /constants/set` | `Constant.Set` | Array of committed values and versions |

List accepts `search`, `value_type`, `limit`, and `offset` query parameters.
The default limit is 100; the maximum is 500. `List` and `Detail` return metadata,
not the current value. Retrieve current values with `Get`.

For example, a batch read body is:

```json
{"ids":[10,11]}
```

The response is an array:

```json
[
	{"id":10,"code":"allow_order_creation","value":true,"version":1},
	{"id":11,"code":"default_price_type","value":{"id":1,"name":"Розничная"},"version":4}
]
```

Use the returned versions when saving:

```json
{
	"items":[
		{"id":10,"value":false,"expected_version":1},
		{"id":11,"value":{"id":2,"name":"Оптовая"},"expected_version":4}
	]
}
```

The batch is atomic: a validation error, missing ID, or version conflict prevents
all assignments. `expected_version` is optional; omitting it lets the manager
read a current version before validation, while still detecting an intervening
write. Repeating an ID in a write batch is rejected. Batches accept up to 500
IDs, and HTTP JSON request bodies are limited to 1 MiB.

An unchanged assignment leaves its row version and modification time unchanged.
Changing metadata or the value through SQL increments the row version.

## WebSocket requests

Use the existing webapp WebSocket bridge and allow the four registered service
methods in the project's method policy. The same service authorizer still runs.

```json
{
	"f":"Constant.Get",
	"q":"constants-1",
	"p":{"ids":[10,11]}
}
```

To save:

```json
{
	"f":"Constant.Set",
	"q":"constants-2",
	"p":{"items":[{"id":10,"value":false,"expected_version":1}]}
}
```

`Constant.List` takes an object such as `{"limit":100,"offset":0}`;
`Constant.Detail` takes `{"id":10}`. Responses use the existing
`event_id: "Response"` envelope and query ID.

## Cache and multi-server behavior

`Get` and typed reads use the manager's local cache. Missing or invalidated IDs
are fetched together from the primary. `Set` always writes to PostgreSQL and
commits before reporting success; there is no write-behind queue.

The database trigger emits `webapp_constants_changed` in the write transaction.
The small notification identifies the row and version and does not contain its
value. Every running manager subscribed to the same database receives the hint,
invalidates the affected entry, and refreshes it. The writer also updates its own
cache after commit, without waiting for its notification to return.

Notification-driven background work coalesces changes and refreshes the whole
catalogue. This keeps the small catalogue and derived-state hooks synchronized;
request-side misses can still load only the required IDs in one query.

The manager protects cache publication against older in-flight loads and older
write responses, so a late result cannot replace a newer invalidation/version.
Cache synchronization is internal; projects do not have to manually invalidate
this common cache.

Notifications provide eventual consistency across processes. A read can briefly
return an earlier value while another server's notification is in flight.
Batch reads from independent cached entries do not promise a single database
transaction snapshot. Put settings that must always be observed together in one
structured JSON constant, or read the required authoritative state in a project
database transaction.

Notifications are not a durable event history. The manager refreshes after
reconnecting and periodically reconciles to catch missed notifications or SQL
changes. `ReconcileInterval` defaults to one minute. `Refresh(ctx)` requests a
manual reconciliation. Observe asynchronous failures through `OnError` and use
an appropriate freshness policy for the application's requirements.

Known-invalidated entries are never served as stale values. If a listener
disconnects, the manager invalidates the catalogue and attempts to reconnect and
refresh. A read of an invalidated entry must succeed against the primary or
return an error. Previously valid cache hits can survive a failed periodic
refresh until `MaxStale` is reached; after that, reads force a full refresh and
fail with `ErrUnsynced` if it cannot complete. No cache can promise a bounded
fresh value during an unavailable database.

## Reacting to changes in project code

Projects only need a subscription when they maintain derived state or need an
additional reaction, such as invalidating a calculated price cache. Register
before `Start` to receive initial resynchronization:

```go
unsubscribe, err := manager.Subscribe("default_price_type",
	func(ctx context.Context, event constants.ChangeEvent) error {
		// The common cache has already been refreshed. GetValue can now read
		// the current configuration, or the handler can invalidate derived data.
		return priceCalculator.Invalidate(ctx)
	},
)
if err != nil {
	return err
}
defer unsubscribe()
if err := manager.Start(appLifetimeContext); err != nil {
	return err
}
```

`ChangeEvent` includes `Code`, `ID`, `Version`, `Deleted`, and `Resync`.
An empty subscription code matches every constant. Handlers run outside cache
locks and the PostgreSQL listener loop. They should respect their context and
be idempotent: events can be coalesced or repeated, and reconnect reconciliation
may skip intermediate values while applying the latest state.

A failed handler is reported through `OnError` and retried against the current
validated state with exponential backoff. Retries begin after
`CallbackRetryInterval` and increase to at most one minute. The retry reconciles
the latest value rather than replaying the failed historical version. All
subscribers for that code may run again when one subscriber fails, including
subscribers that already succeeded. Handlers must therefore make repeated calls
safe and return when their context is canceled. A callback context deadline is
cooperative; it cannot forcibly stop a handler that ignores the context.

Use `Deleted` to remove derived state for a removed optional constant. Required
constants should remain present while the corresponding application version
is running. A callback is a current-state reconciliation hook, not a reliable
job for every historical edit. Business actions requiring durable retries belong
in a project outbox/worker.

## Errors and tests

Trusted Go callers can use `errors.Is` with `ErrNotFound`, `ErrConflict`,
`ErrValidation`, `ErrDefinition`, `ErrNotStarted`, `ErrClosed`, and `ErrUnsynced`.
The administration service maps expected failures to HTTP/WebSocket errors:

| Failure | HTTP status | Public code |
| --- | --- | --- |
| Missing constant | 404 | `constant_not_found` |
| Concurrent edit | 409 | `constant_conflict` |
| Invalid value/request | 400 | `constant_validation` or `bad_request` |
| Manager unavailable | 503 | `constants_unavailable` |
| Unexpected/definition/database failure | 500 | `constants_error` |

Authentication/authorization errors supplied as `webapp.HTTPError` retain their
status. Internal validator/database details remain in the error chain for server
logging rather than being included in a public response.

Run the ordinary library tests with `go test ./...` and race checks with
`go test -race ./...`. PostgreSQL integration tests use
`WEBAPP_CONSTANTS_TEST_DATABASE_URL`; point it at a dedicated test database.
The role needs `CREATEDB`: integration tests create and drop isolated disposable
databases and do not modify tables in the URL's existing database.
The supplied project example is compiled with the package tests.

The archive retains the library's existing local `replace` directives in
`go.mod`. If your checkout uses a different local dependency layout, adjust them
locally or use an alternate modfile/workspace before running tests.
