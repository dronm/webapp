package webapp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dronm/modelbind/types"
)

type compositeDeleteKey struct {
	GroupCode string `json:"group_code"`
	ItemCode  string `json:"item_code"`
}

func (compositeDeleteKey) Relation() string {
	return "public.catalogue_items"
}

type simpleDeleteKey struct {
	ID int `json:"id"`
}

func (simpleDeleteKey) Relation() string {
	return "public.test_models"
}

type otherDeleteKey struct {
	ID int `json:"id"`
}

func (otherDeleteKey) Relation() string {
	return "public.other_models"
}

type emptyDeleteKey struct {
	Ignored string `json:"-"`
}

func (emptyDeleteKey) Relation() string {
	return "public.test_models"
}

func TestBuildDeleteModelQueryCompositeKey(t *testing.T) {
	queryText, queryParams, err := buildDeleteModelQuery([]types.DBModel{
		compositeDeleteKey{
			GroupCode: "vehicle_region",
			ItemCode:  "test-region",
		},
	})
	if err != nil {
		t.Fatalf("buildDeleteModelQuery() error = %v", err)
	}

	wantQuery := "DELETE FROM public.catalogue_items WHERE ((group_code = $1) AND (item_code = $2))"
	if queryText != wantQuery {
		t.Fatalf("query = %q, want %q", queryText, wantQuery)
	}

	wantParams := []any{"vehicle_region", "test-region"}
	if !reflect.DeepEqual(queryParams, wantParams) {
		t.Fatalf("params = %#v, want %#v", queryParams, wantParams)
	}
}

func TestBuildDeleteModelQueryMultipleCompositeKeys(t *testing.T) {
	queryText, queryParams, err := buildDeleteModelQuery([]types.DBModel{
		compositeDeleteKey{GroupCode: "vehicle_region", ItemCode: "first"},
		compositeDeleteKey{GroupCode: "vehicle_region", ItemCode: "second"},
	})
	if err != nil {
		t.Fatalf("buildDeleteModelQuery() error = %v", err)
	}

	wantQuery := "DELETE FROM public.catalogue_items WHERE ((group_code = $1) AND (item_code = $2)) OR ((group_code = $3) AND (item_code = $4))"
	if queryText != wantQuery {
		t.Fatalf("query = %q, want %q", queryText, wantQuery)
	}

	wantParams := []any{"vehicle_region", "first", "vehicle_region", "second"}
	if !reflect.DeepEqual(queryParams, wantParams) {
		t.Fatalf("params = %#v, want %#v", queryParams, wantParams)
	}
}

func TestBuildDeleteModelQueryMultipleSimpleKeys(t *testing.T) {
	queryText, queryParams, err := buildDeleteModelQuery([]types.DBModel{
		simpleDeleteKey{ID: 10},
		simpleDeleteKey{ID: 20},
	})
	if err != nil {
		t.Fatalf("buildDeleteModelQuery() error = %v", err)
	}

	wantQuery := "DELETE FROM public.test_models WHERE (id = $1) OR (id = $2)"
	if queryText != wantQuery {
		t.Fatalf("query = %q, want %q", queryText, wantQuery)
	}

	wantParams := []any{10, 20}
	if !reflect.DeepEqual(queryParams, wantParams) {
		t.Fatalf("params = %#v, want %#v", queryParams, wantParams)
	}
}

func TestBuildDeleteModelQueryRejectsDifferentRelations(t *testing.T) {
	_, _, err := buildDeleteModelQuery([]types.DBModel{
		simpleDeleteKey{ID: 10},
		otherDeleteKey{ID: 20},
	})
	if err == nil {
		t.Fatal("buildDeleteModelQuery() error = nil, want relation mismatch error")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %q, want relation mismatch", err)
	}
}

func TestBuildDeleteModelQueryRejectsKeyWithoutFields(t *testing.T) {
	_, _, err := buildDeleteModelQuery([]types.DBModel{
		emptyDeleteKey{Ignored: "value"},
	})
	if err == nil {
		t.Fatal("buildDeleteModelQuery() error = nil, want empty key error")
	}
	if !strings.Contains(err.Error(), "has no fields") {
		t.Fatalf("error = %q, want empty key error", err)
	}
}
