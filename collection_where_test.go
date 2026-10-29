package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
)

func TestCollectionWhereQueryFormsAndLegacyCompatibility(t *testing.T) {
	legacy := `{"filter":[{"j":"or","f":{"name":{"o":"ilk","v":"%one%","ignored":true}},"ignored":true}],"sorter":[{"f":"name","d":"d","ignored":true}],"from":3,"count":7,"extra":true}`
	where := `{"or":[{"f":"name","o":"l","v":"z"},{"and":[{"f":"name","o":"e","v":"z"},{"f":"name","o":"g","v":"a"}]}]}`
	for _, packed := range []bool{false, true} {
		values := url.Values{}
		if packed {
			values.Set("params", strings.TrimSuffix(legacy, "}")+`,"where":`+where+`}`)
			// params is authoritative, including ignoring conflicting individual where.
			values.Set("where", `{"and":[]}`)
		} else {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(legacy), &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range fields {
				values.Set(key, string(value))
			}
			values.Set("where", where)
		}
		req := httptest.NewRequest("GET", "/orders?"+values.Encode(), nil)
		old, err := ParseCollectionParams(req)
		if err != nil {
			t.Fatal(err)
		}
		extended, err := ParseCollectionParamsWithWhere(req)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(old, extended.CollectionParams) || string(extended.Where) != where {
			t.Fatalf("%#v %#v", old, extended)
		}
		bound, err := CollectionParamsWithWhereBinder()(req)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(bound, extended) {
			t.Fatalf("%#v", bound)
		}
	}
	// No where: the opt-in path produces exactly the same SQL and parameters.
	req := httptest.NewRequest("GET", "/orders?params="+url.QueryEscape(legacy), nil)
	extended, err := ParseCollectionParamsWithWhere(req)
	if err != nil {
		t.Fatal(err)
	}
	old, err := prepareCollectionModelSelect(accessTestContext(t), &accessOrderList{}, extended.CollectionParams)
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := prepareCollectionModelSelectWithWhere(accessTestContext(t), &accessOrderList{}, extended)
	if err != nil {
		t.Fatal(err)
	}
	a, b := []any{}, []any{}
	q1, t1, e1 := old.BuildCollectionSQL(&a)
	q2, t2, e2 := grouped.BuildCollectionSQL(&b)
	if e1 != nil || e2 != nil || q1 != q2 || t1 != t2 || !reflect.DeepEqual(a, b) {
		t.Fatalf("%s %s %v %v", q1, q2, e1, e2)
	}
}

func TestCollectionWhereLegacyParserStillIgnoresUnknownWhere(t *testing.T) {
	for _, query := range []string{"where=" + url.QueryEscape(`{"malformed":true}`), "params=" + url.QueryEscape(`{"where":{"and":[]},"count":7}`)} {
		req := httptest.NewRequest("GET", "/?"+query, nil)
		if _, err := ParseCollectionParams(req); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseCollectionParamsWithWhere(req); err == nil {
			t.Fatal("extended parser accepted malformed where")
		}
	}
	for _, query := range []string{"where=", "where=null", "where=%7B", "where=%7B%7D&where=%7B%7D", "from=bad", "count=bad", "filter=%7B", "sorter=%7B", "params=%7B"} {
		if _, err := ParseCollectionParamsWithWhere(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Fatal(query)
		}
	}
	if _, err := ParseCollectionParamsWithWhere(nil); err == nil {
		t.Fatal("nil request")
	}
}

func TestCollectionWherePolicyAndAggregateScope(t *testing.T) {
	ctx := accessTestContext(t)
	params := modelbind.CollectionParamsWithWhere{
		CollectionParams: modelbind.CollectionParams{Filter: []modelbind.CollectionFilter{
			{Join: modelbind.FilterParJoinAnd, Fields: map[string]modelbind.CollectionFilterField{"name": {Operator: modelbind.FilterOperParEq, Value: "one"}}},
			{Join: modelbind.FilterParJoinOr, Fields: map[string]modelbind.CollectionFilterField{"name": {Operator: modelbind.FilterOperParEq, Value: "two"}}},
		}},
		Where: json.RawMessage(`{"or":[{"f":"name","o":"l","v":"z"},{"and":[{"f":"name","o":"e","v":"z"},{"f":"name","o":"g","v":"a"}]}]}`),
	}
	builder, err := prepareCollectionModelSelectWithWhere(ctx, &accessOrderList{}, params)
	if err != nil {
		t.Fatal(err)
	}
	args := []any{}
	query, totals, err := builder.BuildCollectionSQL(&args)
	if err != nil {
		t.Fatal(err)
	}
	want := ` WHERE (((name = $1) OR (name = $2)) AND (o.customer_id = $3)) AND ((name < $4) OR ((name = $5) AND (name > $6)))`
	for _, sql := range []string{query, totals} {
		if !strings.Contains(sql, want) {
			t.Fatal(sql)
		}
	}
	if !strings.HasSuffix(query, " LIMIT 100") || strings.Contains(totals, " LIMIT ") {
		t.Fatalf("%s %s", query, totals)
	}
	if !reflect.DeepEqual(args, []any{"one", "two", int64(42), "z", "z", "a"}) {
		t.Fatal(args)
	}
}

// The real collection executor must issue both queries with the same arguments,
// scan their results and retain the original response model shape.
type whereRows struct {
	ds.Rows
	values [][]any
	index  int
}

func (r *whereRows) Next() bool { r.index++; return r.index <= len(r.values) }
func (r *whereRows) Scan(dest ...any) error {
	for i, value := range r.values[r.index-1] {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}
func (*whereRows) Close() error { return nil }
func (*whereRows) Err() error   { return nil }

type whereCollectionConn struct {
	ds.Conn
	queries []string
	args    [][]any
}

func (c *whereCollectionConn) Query(_ context.Context, sql string, args ...any) (ds.Rows, error) {
	c.queries = append(c.queries, sql)
	c.args = append(c.args, append([]any(nil), args...))
	if strings.HasPrefix(sql, "SELECT count(*)") {
		return &whereRows{values: [][]any{{int64(1)}}}, nil
	}
	return &whereRows{values: [][]any{{"one"}}}, nil
}

func TestFetchCollectionWhereExecutesDataAndTotals(t *testing.T) {
	conn := &whereCollectionConn{}
	params := modelbind.CollectionParamsWithWhere{Where: json.RawMessage(`{"f":"name","o":"e","v":"one"}`)}
	data, totals, err := FetchCollectionModelWhereWithConn(accessTestContext(t), conn, &accessOrderList{}, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0].Name != "one" || totals == nil {
		t.Fatalf("%#v %#v", data, totals)
	}
	if len(conn.queries) != 2 || !reflect.DeepEqual(conn.args[0], conn.args[1]) {
		t.Fatalf("%#v %#v", conn.queries, conn.args)
	}
	for _, sql := range conn.queries {
		if !strings.Contains(sql, "WHERE ((o.customer_id = $1)) AND (name = $2)") {
			t.Fatal(sql)
		}
	}
}

func TestCollectionWhereRejectsBeforeQueryAndPreservesDeniedPolicy(t *testing.T) {
	conn := &whereCollectionConn{}
	for _, raw := range []string{`{"f":"missing","o":"e","v":1}`, `{"f":"name","o":"bad","v":"one"}`, `{"and":[]}`} {
		_, _, err := FetchCollectionModelWhereWithConn(accessTestContext(t), conn, &accessOrderList{}, modelbind.CollectionParamsWithWhere{Where: json.RawMessage(raw)})
		if err == nil || len(conn.queries) != 0 {
			t.Fatalf("%v %#v", err, conn.queries)
		}
	}
	// Marked resources without a configured controller fail before DB acquisition.
	_, _, err := FetchCollectionModelWhere(context.Background(), nil, &accessOrderList{}, modelbind.CollectionParamsWithWhere{})
	if !errors.Is(err, ErrAccessConfiguration) {
		t.Fatal(err)
	}
}
