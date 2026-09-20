# webapp access-policy integration: implementation and verification

## Source and dependencies

This change is based on the supplied `webapp(7).zip`. It requires the earlier
policy-enabled `modelbind` from `modelbind-access-policies.zip`; the original
modelbind version does not provide the required APIs.

The deliverable does not change `go.mod`, `go.sum`, modelbind source, codegen,
application schemas, application models, or database migrations. In particular,
the original Go 1.25.0 requirement and local replacements remain intact. The
modelbind replacement must point at a checkout containing the policy patch.
Consuming applications must also resolve that checkout or an actual published
revision containing it. No unpublished version or commit ID was invented.

## Implemented scope

- `access.go`, `access_registry.go`: trusted principals, model bindings, operation-
  specific application/role/user policies, explicit protected-resource markers,
  independent request/message caches, and deny-on-missing-configuration behavior.
- `access_input.go`, `binder.go`: effective-input preparation before required-field
  validation, preserving original decoded model values and presence metadata.
  Nested values not changed by an overlay retain modelbind's copying semantics.
- `crud.go`, `access_delete.go`: policy enforcement in every provider-based and
  `WithConn` CRUD variant, protected detail/list/totals SQL, separate caller-key
  checks, grouped composite-key deletion, and explicit zero-row handling.
- `access_service_input.go`, `service_input.go`: context-aware JSON dispatch and
  registered typed decoders for model inputs and update-by-key envelopes.
- `handler.go`, `service.go`, `route.go`: controller and identity propagation before
  binding and service invocation, without replacing route/method permissions.
- `ws/server.go`, `events/server.go`: a fresh access scope per dispatched message
  and the common context-aware JSON service path. WebSocket message-session
  installation invalidates inherited identity and cached policies.
- `access_errors.go`, `jsonResponse.go`: sanitized authorization responses while
  retaining errors.Is/errors.As for internal handling.
- `notification.go`: ordinary automatic broadcasts are withheld after protected
  access unless the publisher implements the recipient-aware integration hook.
- `access_test.go`, `access_transport_test.go`, `access_example_test.go`: regression
  tests and executable API examples.

See `ACCESS_POLICIES.md` for configuration and exact API semantics.

## Verification actually performed

Native verification was attempted in the unmodified delivery tree with:

```bash
GOTOOLCHAIN=local go test ./...
```

It could not run:

```text
go: go.mod requires go >= 1.25.0 (running go 1.23.2; GOTOOLCHAIN=local)
```

The environment also lacked the uploaded module's local dependency checkouts and
could not download the required toolchain/dependencies. Consequently, the actual
Go 1.25 build and the pinned external dependencies have NOT been verified here.

An isolated compatibility copy was used for additional checks. In that copy only:

1. The Go directive was lowered to Go 1.23.
2. The pre-existing modelbind `strings.SplitSeq` loop was replaced with the
   equivalent `strings.Split` iteration available in that toolchain.
3. External `ds/v4`, `session`, `pgx/v5`, and Gorilla WebSocket modules were replaced
   by minimal API shims sufficient to compile the packages and run the tests.
   The shims do not implement a real connection pool, database, session backend,
   or WebSocket network stack.

The actual policy-enabled modelbind implementation and webapp source were used,
subject to the compatibility-only changes above. These commands passed there:

```bash
go test -race -shuffle=on -count=10 ./...
go vet ./...
```

The corresponding isolated modelbind copy also passed:

```bash
go test -race ./...
```

The webapp test listing contains 65 named tests and two executable examples across
its packages, including 28 new test functions and two new examples. Many functions
have additional table-driven subtests. These counts describe what was run in the
compatibility copy; they are not a claim of native dependency compatibility.

Coverage includes missing authorization context across all 14 CRUD entry points,
role/user intersections, failed lookup caching, concurrent snapshot access,
required server-supplied fields, explicit NULL/conflicting ownership values,
DTO-hidden write columns, detail/list/totals restrictions, caller OR grouping,
composite-key deletes, caller-key safeguards, zero-row outcomes, HTTP error status
handling, typed JSON presence tracking, per-message identity refresh, and protected
notification suppression. Database writes in these tests use fake connections;
read-query preparation is tested through the same internal preparation functions
called by the CRUD execution paths.

No compatibility edits, dependency shims, local logs, or replacement test modules
are included in the delivered library or patch.

### Not verified in this environment

- A native Go 1.25+ build against the actual external dependency versions.
- Live PostgreSQL query execution, query plans, triggers, transactions, or RLS.
- Real connection-pool behavior, session backend behavior, or WebSocket network
  upgrades/message delivery. Shared dispatch is unit-tested; real transports are
  not integration-tested.
- A consuming application's generated model set, route definitions, role data,
  SQL projections, or production authorization configuration.

Run the following in the normal development environment after installing the
patched modelbind checkout:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Then exercise the application's read/detail/write/batch and WebSocket paths against
its test database, including unauthorized and mixed-access requests.

## Deliberate boundaries and remaining integration

Protection is opt-in. Every writable/read/key representation of a protected
resource must implement `AccessResource()` and have a registered model binding.
A marked resource fails without configuration. An unmarked legacy resource remains
unprotected; the known-relation guard is additional detection, not global SQL
interception.

Application code must provide authentication-aware principal resolution, policy
factories, current role/user assignments, and `AppContext.Access`. Roles combine
as restrictions with AND. Different write rules for the same logical field are
configuration errors; calculate an intended combined rule inside a factory rather
than relying on implicit set union/intersection or precedence.

Standard HTTP write binders and typed JSON decoders prepare effective input before
required validation. Direct service callers that validate input before calling a
CRUD helper should explicitly call `PrepareAccessInput` first. Context propagation
does not infer which operation an arbitrary custom service method performs.
Protected `ModelInput` JSON methods require explicit typed decoder registration.

Batch deletion affects the permitted matching keys and returns the affected count.
It is NOT an all-or-nothing authorization operation for the entire requested batch.
Applications requiring that behavior must own the transaction and concurrency
checks. Missing and inaccessible individual records have the same protected
not-found response.

Automatic notifications after protected access are suppressed for a publisher
implementing only the old `EventPublisher` interface. `AccessEventPublisher` is a
hook: its implementation must filter recipients and sanitize payloads. It does
not provide recipient authorization by itself. Explicit notifications and external
database notifications remain the application's responsibility.

Arbitrary SQL, unscoped subqueries/aggregates, cross-table business rules, final
trigger-modified row values, and native PostgreSQL RLS remain outside these helpers.
This patch does not rewrite raw SQL and does not make them equivalent to a database-
level security boundary.
