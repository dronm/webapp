# Opt-in grouped collection filters

Existing routes, request types, binders, collection helpers and responses remain
unchanged. Updating the libraries alone does not enable grouped conditions.
The original `CollectionParamsBinder` and `ParseCollectionParams` keep ignoring
unknown fields, including `where`. The new API accepts existing requests too.

## Minimal application changes

Update modelbind and webapp together. When developing from sibling checkouts,
point webapp at the updated modelbind using your existing local replacement, or:

```sh
go mod edit -replace=github.com/dronm/modelbind=../modelbind
```

After publishing, pin webapp's modelbind dependency to the published revision.
The supplied repository's development `go.mod` replacement paths are retained;
no nonexistent release version has been invented.

On each route that should opt in, make three changes:

1. Replace `webapp.CollectionParamsBinder()` with
   `webapp.CollectionParamsWithWhereBinder()`.
2. Change the service method argument from `modelbind.CollectionParams` to
   `modelbind.CollectionParamsWithWhere`.
3. Replace `webapp.FetchCollectionModel` with `webapp.FetchCollectionModelWhere`,
   or `webapp.FetchCollectionModelWithConn` with
   `webapp.FetchCollectionModelWhereWithConn`.

```go
route.Binder = webapp.CollectionParamsWithWhereBinder()

// Service method; keep the application's existing response envelope.
func (s *OrderService) List(
	ctx context.Context,
	params modelbind.CollectionParamsWithWhere,
) (models.CollectionResponse[*OrderList], error) {
	rows, aggregate, err := webapp.FetchCollectionModelWhere(ctx, s.DB, &OrderList{}, params)
	if err != nil {
		return models.CollectionResponse[*OrderList]{}, err
	}
	return models.CollectionResponse[*OrderList]{Rows: rows, Agg: aggregate}, nil
}
```

The same endpoint, model, access registry, filters, sorters, default count of 100,
limits and response models are used. No additional endpoint, token or scrolling
code is needed. Mark date-only `time.Time` model fields with `dateType:"date"`.

## Request/query forms

Both existing forms are supported:

- `?params=<URL-encoded JSON object>`: optionally include `where` in the object.
- `?filter=<JSON array>&sorter=<JSON array>&from=0&count=100&where=<JSON expression>`.

As before, a non-empty `params` takes precedence over all individual query
parameters. Unknown top-level and legacy filter/sorter properties are still
ignored. The new `where` expression itself is strict. Omit it for legacy-only
behavior; explicit null or an empty individual `where` is invalid. This binder
continues to read query parameters; it does not introduce JSON-body precedence.

`ParseCollectionParamsWithWhere` is the standalone parser. Structural/operator
errors are caught by the binder. Model field and value validation occurs during
collection binding, before SQL execution, with paths such as
`where.or[1].and[0]`. New nodes must be either a non-empty `and`/`or` group or a
leaf containing exactly `f`, `o`, `v`. Nesting is limited to 64 edges below the
root. Duplicate/unknown node members and invalid fields/operators fail.

## Examples

[legacy.json](../examples/grouped-filters/legacy.json) is an existing request
that works unchanged. It filters customer 10 and sorts IDs descending.

[date-id-cursor.json](../examples/grouped-filters/date-id-cursor.json) contains
this grouped condition plus ordinary sorting/limits:

```json
{
	"and": [
		{ "f": "customer_id", "o": "e", "v": 10 },
		{
			"or": [
				{ "f": "order_date", "o": "l", "v": "2026-10-29" },
				{
					"and": [
						{ "f": "order_date", "o": "e", "v": "2026-10-29" },
						{ "f": "id", "o": "l", "v": 1500 }
					]
				}
			]
		}
	]
}
```

Use sorting `order_date DESC, id DESC`, `from: 0`, `count: 100`. The equivalent SQL
is `customer_id = $1 AND (order_date < $2 OR (order_date = $3 AND id < $4))`,
followed by that ORDER BY and limit. Parameter values are bound separately.
The application constructs the next bound from the last row; sorting must end
in a unique key. Ascending/mixed sorting and nullable keys need corresponding
explicit conditions.

[search-and-cursor.json](../examples/grouped-filters/search-and-cursor.json)
combines legacy `name ILIKE '%apple%' OR comment ILIKE '%apple%'` filters with a
grouped date-and-ID cursor. Both search columns must be in model metadata.
The result is `(entire legacy search) AND (entire cursor expression)`, retaining
legacy SQL precedence, default joins, and operator behavior.

For example, send a complete example object using curl from the repository root:

```sh
curl --get 'http://localhost:8080/orders' \
	--data-urlencode 'params@examples/grouped-filters/date-id-cursor.json'
```

All existing collection operators remain available inside groups. Null tests
use `i` / `in` with `v: null`; equality with null retains the old parameterized
comparison behavior. Array/FTS compilation is reused without changes. In these
versions `incl` is `column = ANY(parameter_array)`, while the existing `any`/`has`
SQL-operator alias follows `parameter = ANY(array_column)` for both codes.

Authorization is resolved and installed independently using the existing
`ResolvedAccess.ApplyTo` path. Its read-builder type check also recognizes the
new `PgGroupedSelect`. Client OR groups cannot weaken policy restrictions.
Data and aggregate queries share the same WHERE and arguments; aggregates include
cursor bounds before the page limit. Sort/offset/count only apply to data.

For custom SQL services, use modelbind's `CollectionParamsWithWhere`,
`pg.NewPgGroupedSelect` and `BindCollectionSelectModelWithWhere`, and build SQL
through the wrapper. Neither legacy interfaces nor access.Predicate are extended.
The latter has different null/array semantics and lacks several collection
operators; new grouped leaves instead reuse the legacy collection compiler.

## Verification

Run `go test ./...` and `go test -race ./...` after resolving this repository's
existing local development replacements and pointing modelbind at the updated
checkout. Unit and execution tests cover both query forms, old request semantics,
legacy mixed joins, malformed groups, authorization, exact no-where delegation,
and shared data/aggregate arguments.

The optional PostgreSQL test uses a session-local temporary table only:

```sh
WEBAPP_COLLECTION_TEST_DATABASE_URL='postgres://user:password@localhost/testdb' \
	go test -run '^TestCollectionWherePostgres$' -v .
```

It verifies repeated dates across cursor pages, stable ID ordering, legacy
search plus bounds, policy isolation, grouped null/membership/text operators,
and totals before limiting. Without this environment variable it is explicitly
skipped. Existing constants PostgreSQL tests retain their separate
`WEBAPP_CONSTANTS_TEST_DATABASE_URL` configuration.
