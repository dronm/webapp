# webapp

`webapp` is a small `net/http` route, binding, service, session, event, and WebSocket helper package.

It is intended to replace Gin-based controller boilerplate with this flow:

1. define an explicit `net/http` route;
2. bind and validate request input with `github.com/dronm/modelbind`;
3. construct a request-scoped service;
4. call one registered service method;
5. return JSON or a standard JSON error.

The same service registry is used by HTTP routes, WebSocket messages, and PostgreSQL `LISTEN/NOTIFY` local event calls.

## Access policies

Opt-in user/role presets now restrict modelbind CRUD reads and writes, including
single-record reads, totals, both full-model and ModelInput write helpers, and
batch deletes. Standard write binders supply permitted server values before
required-field validation. Marked resources fail closed without configuration.

See [Access policies](docs/ACCESS_POLICIES.md) for model markers, explicit field
bindings, principal resolution, HTTP/WebSocket integration, notification behavior
and dependency setup. See [Implementation notes](docs/IMPLEMENTATION_NOTES.md)
for test coverage and verification limits. This requires the policy-enabled
modelbind patch; codegen/application registration is not automatic.

## Packages

```text
webapp/          root route, binder, middleware, service registry, JSON response helpers
webapp/database PostgreSQL provider initialization
webapp/constants typed application constants, shared cache and PostgreSQL synchronization
webapp/session  Redis-backed HTTP session resolver
webapp/events   PostgreSQL LISTEN/NOTIFY event server and Event service
webapp/ws       WebSocket bridge to registered webapp services
```

## Application constants

`webapp/constants` provides a PostgreSQL catalogue with stable codes, JSONB
values, logical types, project-owned Go validators, typed accessors, and generic
`List`, `Detail`, batch `Get`, and atomic batch `Set` service methods. One shared
manager caches values in each process and synchronizes through PostgreSQL
notifications, reconnect refreshes, and periodic reconciliation.

See [Constants](docs/CONSTANTS.md) for migrations, authorization, lifecycle,
HTTP/WebSocket requests, cache guarantees, and project integration. The compiled
[project example](examples/constants/project.go) includes a concrete
`DefaultPriceTypeService`; [seed.sql](examples/constants/seed.sql) supplies initial
values without replacing administrator changes.

## Service method signatures

Registered service methods may use one of these signatures:

```go
func (s *Service) Method(ctx context.Context) error
func (s *Service) Method(ctx context.Context, input Input) error
func (s *Service) Method(ctx context.Context) (Result, error)
func (s *Service) Method(ctx context.Context, input Input) (Result, error)
```

## Route example

```go
type SupplierService struct {
	DB      *pgds.PgProvider
	Session session.Session
}

func NewSupplierService(ctx webapp.ServiceContext) any {
	return &SupplierService{
		DB:      ctx.DB,
		Session: ctx.Session,
	}
}

func init() {
	webapp.MustRegisterService("Supplier", &SupplierService{}, NewSupplierService)
}

func BuildSupplierRoutes(appCtx *webapp.AppContext) http.Handler {
	routes := []webapp.Route{}
	suppliers := webapp.NewGroup("/supplier", &routes)

	suppliers.GET(
		"/list",
		webapp.WithService("Supplier", "FetchList"),
		webapp.WithBinder(webapp.CollectionParamsBinder()),
	)
	suppliers.GET(
		"/{id}",
		webapp.WithService("Supplier", "FetchDetail"),
		webapp.WithBinder(webapp.PathValueBinder[int]("id")),
	)
	suppliers.POST(
		"/add",
		webapp.WithService("Supplier", "Insert"),
		webapp.WithBinder(webapp.InsertInputBinder[*models.Supplier]()),
	)
	suppliers.PATCH(
		"/{id}",
		webapp.WithService("Supplier", "Update"),
		webapp.WithBinder(webapp.QueryAndPathInputBinder[*models.Supplier](false, "id")),
	)

	return webapp.MustBuildRouter(
		appCtx,
		routes,
		webapp.RecoverMiddleware(),
		webapp.QueryIDMiddleware(""),
		webapp.CORSMiddleware(webapp.CORSConfig{
			AllowedOrigins:   []string{"https://example.com"},
			AllowCredentials: true,
		}),
		webapp.TimeoutMiddleware(30*time.Second),
	)
}
```

## Redis session middleware

```go
sessManager, err := session.NewRedisManager(session.Config{
	CookieName:      "_s",
	CookieDomain:    ".example.com",
	Secure:          true,
	HTTPOnly:        true,
	SameSite:        http.SameSiteLaxMode,
	MaxLifeTime:     86400,
	MaxIdleTime:     3600,
	DestroyAllTime: "03:00",
}, session.RedisConfig{
	Connect:   "127.0.0.1:6379",
	Namespace: "myapp",
})
if err != nil {
	return err
}

handler := webapp.MustBuildRouter(
	appCtx,
	routes,
	webapp.RecoverMiddleware(),
	webapp.QueryIDMiddleware(""),
	webapp.CORSMiddleware(corsCfg),
	webapp.TimeoutMiddleware(30*time.Second),
	webapp.SessionMiddleware(sessManager.Resolver),
)
```

Put CORS before session middleware so `OPTIONS` preflight requests do not create sessions.

## WebSocket bridge

Client messages use the old compatible shape:

```json
{
	"f": "Supplier.FetchDetail",
	"q": "query-1",
	"p": 10
}
```

Server responses use:

```json
{
	"event_id": "Response",
	"query_id": "query-1",
	"payload": {}
}
```

Wiring:

```go
wsServer := ws.NewServer(ws.Config{
	AppContext:      appCtx,
	SessionResolver: sessManager.Resolver,
	EventBus:        eventServer,
	CheckOrigin: func(origin string) bool {
		return origin == "https://example.com"
	},
	IsMethodAllowed: func(sess session.Session, method string) error {
		return nil
	},
})

mux.Handle("/ws", wsServer.Handler())
```

## PostgreSQL events

```go
eventServer := events.NewServer(events.Config{
	Pool:       pool,
	AppContext: appCtx,
	LocalEvents: map[string]events.Target{
		"Order.ReserveExpired": {
			Service: "Order",
			Method:  "ReserveExpired",
		},
	},
})
eventServer.SetPublisher(wsServer)
eventServer.Start(context.Background())
```

Local event payloads are decoded by `webapp.DecodeInputForService`, so the payload must be valid JSON for the registered service method input type.

## Event subscription service

```go
webapp.MustRegisterService(
	"Event",
	&events.Service{},
	func(ctx webapp.ServiceContext) any {
		return events.NewService(ctx, wsServer)
	},
)
```

Then WebSocket clients may call:

```json
{
	"f": "Event.Subscribe",
	"q": "sub-1",
	"p": {
		"events": ["Order.Changed"]
	}
}
```

## Binder choice

Use `InsertInputBinder[T]` or `UpdateInputBinder[T]` when your service needs `modelbind.ModelInput[T]`, including absent-field metadata.

Use `InsertModelBinder[T]` or `UpdateModelBinder[T]` when your service only needs `T`.

Use `PathValueBinder[T]("id")` when the service accepts a scalar path value directly.

Use `CollectionParamsBinder()` for list routes that expect `modelbind.CollectionParams`.

## Database constraint errors

CRUD helpers normalize recognized PostgreSQL reference-constraint errors before
they reach the HTTP response layer. A delete blocked by SQLSTATE `23001`
(`restrict_violation`) or `23503` (`foreign_key_violation`) returns HTTP
`409 Conflict` with the public error code `record_in_use`. The original
PostgreSQL error remains available to `errors.As` through the returned error
chain, while database details are not included in the response.

A non-nil `DBErrorHandler` remains authoritative for model-specific messages.
Errors from custom SQL are conservatively normalized by `WriteServiceError`;
call `NormalizeDatabaseError` directly when the operation is known and its
operation-specific result is required before the HTTP boundary.

## Grouped collection filters (opt-in)

Nested client `where` conditions are available through additive request and binding
APIs. Existing filters and APIs keep their behavior. See
[Grouped collection filters](docs/GROUPED_FILTERS.md) for application opt-in,
compound date-and-ID cursor requests, search composition, and tests.
