package webapp

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/jackc/pgx/v5"
)

type wherePostgresOrder struct {
	ID         int       `json:"id"`
	CustomerID int       `json:"customer_id"`
	OrderDate  time.Time `json:"order_date" dateType:"date"`
	Name       *string   `json:"name"`
}

func (*wherePostgresOrder) Relation() string       { return "grouped_filter_test_orders" }
func (*wherePostgresOrder) AccessResource() string { return "GroupedOrder" }
func (*wherePostgresOrder) CollectionAgg() any     { return &wherePostgresTotal{} }

type wherePostgresTotal struct {
	Count int64 `json:"count" agg:"count(*)"`
}

// Use only a session-local temporary table. The supplied database is not
// migrated and no persistent application tables are read or changed.
func TestCollectionWherePostgres(t *testing.T) {
	dsn := os.Getenv("WEBAPP_COLLECTION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WEBAPP_COLLECTION_TEST_DATABASE_URL to run grouped-filter PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx, `CREATE TEMP TABLE grouped_filter_test_orders (id integer PRIMARY KEY, customer_id integer NOT NULL, order_date date NOT NULL, name text);
		INSERT INTO grouped_filter_test_orders VALUES
		(1600,10,'2026-10-29','Apple'), (1500,10,'2026-10-29','Apricot'),
		(1499,10,'2026-10-29','Apple'), (1498,10,'2026-10-29','Banana'),
		(1400,10,'2026-10-28','Apricot'), (1300,10,'2026-10-27',NULL),
		(1200,20,'2026-10-26','Apple');`)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewPolicyRegistry()
	if err := registry.RegisterModel(&wherePostgresOrder{}, modelpg.PolicyBinding{Rows: modelpg.FieldMap{"customer_id": "customer_id"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("GroupedOrder", access.Read, func(context.Context, Principal) (access.Policy, error) {
		return access.Scope(access.Eq("customer_id", 10)), nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx = ContextWithPrincipal(ContextWithAccess(ctx, &AccessController{Registry: registry}), Principal{UserID: "test"})
	adapter := &wherePGConn{conn: conn}
	cursor := json.RawMessage(`{"or":[{"f":"order_date","o":"l","v":"2026-10-29"},{"and":[{"f":"order_date","o":"e","v":"2026-10-29"},{"f":"id","o":"l","v":1500}]}]}`)
	params := modelbind.CollectionParamsWithWhere{
		CollectionParams: modelbind.CollectionParams{
			Filter: []modelbind.CollectionFilter{
				{Join: modelbind.FilterParJoinAnd, Fields: map[string]modelbind.CollectionFilterField{"name": {Operator: modelbind.FilterOperParILk, Value: "A%"}}},
				{Join: modelbind.FilterParJoinOr, Fields: map[string]modelbind.CollectionFilterField{"name": {Operator: modelbind.FilterOperParIs, Value: nil}}},
			},
			Sorter: []modelbind.CollectionSorter{{Field: "order_date", Direct: modelbind.SortParDesc}, {Field: "id", Direct: modelbind.SortParDesc}}, Count: 1,
		}, Where: cursor,
	}
	data, totals, err := FetchCollectionModelWhereWithConn(ctx, adapter, &wherePostgresOrder{}, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0].ID != 1499 || totals.(*wherePostgresTotal).Count != 3 {
		t.Fatalf("%#v %#v", data, totals)
	}
	// The second page starts after (2026-10-29,1499), never repeats that row.
	params.Where = json.RawMessage(`{"or":[{"f":"order_date","o":"l","v":"2026-10-29"},{"and":[{"f":"order_date","o":"e","v":"2026-10-29"},{"f":"id","o":"l","v":1499}]}]}`)
	params.Count = 100
	data, totals, err = FetchCollectionModelWhereWithConn(ctx, adapter, &wherePostgresOrder{}, params)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int{}
	for _, row := range data {
		ids = append(ids, row.ID)
	}
	if !reflect.DeepEqual(ids, []int{1400, 1300}) || totals.(*wherePostgresTotal).Count != 2 {
		t.Fatalf("%#v %#v", ids, totals)
	}
	// Null, case-insensitive text and membership leaves execute inside a group.
	params.Filter = nil
	params.Where = json.RawMessage(`{"and":[{"f":"id","o":"incl","v":[1499,1300,1200]},{"or":[{"f":"name","o":"i","v":null},{"f":"name","o":"ilk","v":"a%"}]}]}`)
	data, totals, err = FetchCollectionModelWhereWithConn(ctx, adapter, &wherePostgresOrder{}, params)
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, row := range data {
		ids = append(ids, row.ID)
	}
	if !reflect.DeepEqual(ids, []int{1499, 1300}) || totals.(*wherePostgresTotal).Count != 2 {
		t.Fatalf("%#v %#v", ids, totals)
	}
}

// A narrow pgx adapter keeps this test on the same ds.Conn execution API as
// applications, while retaining the temporary table's single connection.
type wherePGConn struct {
	ds.Conn
	conn *pgx.Conn
}

func (c *wherePGConn) Query(ctx context.Context, sql string, args ...any) (ds.Rows, error) {
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &wherePGRows{Rows: rows}, nil
}

type wherePGRows struct{ pgx.Rows }

func (r *wherePGRows) Close() error { r.Rows.Close(); return nil }
