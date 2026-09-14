package webapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind/types"
	"github.com/jackc/pgx/v5/pgconn"
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

type failingDeleteConn struct {
	ds.Conn
	execErr error
}

func (c failingDeleteConn) Exec(
	context.Context,
	string,
	...any,
) (ds.ExecResult, error) {
	return nil, c.execErr
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

func TestDeleteModelWithConnMapsReferenceConstraintErrors(t *testing.T) {
	for _, sqlState := range []string{
		"23001",
		"23503",
	} {
		t.Run(sqlState, func(t *testing.T) {
			pgErr := &pgconn.PgError{
				Code:           sqlState,
				Message:        "private database message",
				Detail:         "private database detail",
				ConstraintName: "private_constraint_name",
			}
			execErr := fmt.Errorf("execute delete: %w", pgErr)

			_, err := DeleteModelWithConn(
				context.Background(),
				failingDeleteConn{execErr: execErr},
				[]types.DBModel{simpleDeleteKey{ID: 10}},
				nil,
			)
			if err == nil {
				t.Fatal("DeleteModelWithConn() error = nil, want conflict error")
			}

			var httpErr HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("DeleteModelWithConn() error = %T, want HTTPError", err)
			}
			if httpErr.StatusCode() != http.StatusConflict {
				t.Fatalf("status = %d, want %d", httpErr.StatusCode(), http.StatusConflict)
			}
			if httpErr.Code() != "record_in_use" {
				t.Fatalf("code = %q, want record_in_use", httpErr.Code())
			}
			if httpErr.PublicMessage() != "Невозможно удалить запись: она используется в других данных." {
				t.Fatalf("message = %q, want public record-in-use message", httpErr.PublicMessage())
			}
			if !errors.Is(err, pgErr) {
				t.Fatal("mapped error does not preserve the original PostgreSQL error")
			}

			recorder := httptest.NewRecorder()
			WriteServiceError(recorder, fmt.Errorf("delete contact: %w", err))
			if recorder.Code != http.StatusConflict {
				t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusConflict)
			}

			body := decodeJSONBody[ErrorResponse](t, recorder)
			if body.Error.Code != "record_in_use" {
				t.Fatalf("response code = %q, want record_in_use", body.Error.Code)
			}
			if body.Error.Message != httpErr.PublicMessage() {
				t.Fatalf("response message = %q, want %q", body.Error.Message, httpErr.PublicMessage())
			}
			if strings.Contains(recorder.Body.String(), "private database") ||
				strings.Contains(recorder.Body.String(), "private_constraint_name") {
				t.Fatalf("response leaked private PostgreSQL details: %s", recorder.Body.String())
			}
		})
	}
}

func TestDeleteModelWithConnLeavesOtherErrorsUnchanged(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "other PostgreSQL constraint",
			err:  &pgconn.PgError{Code: "23505"},
		},
		{
			name: "non-PostgreSQL error",
			err:  errors.New("connection failed"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DeleteModelWithConn(
				context.Background(),
				failingDeleteConn{execErr: test.err},
				[]types.DBModel{simpleDeleteKey{ID: 10}},
				nil,
			)
			if err != test.err {
				t.Fatalf("DeleteModelWithConn() error = %v, want original error %v", err, test.err)
			}
		})
	}
}

func TestDeleteModelWithConnPreservesCustomErrorHandler(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23001"}
	customErr := errors.New("custom delete error")
	handlerCalled := false

	_, err := DeleteModelWithConn(
		context.Background(),
		failingDeleteConn{execErr: pgErr},
		[]types.DBModel{simpleDeleteKey{ID: 10}},
		func(err error) error {
			handlerCalled = true
			if err != pgErr {
				t.Fatalf("custom handler error = %v, want original PostgreSQL error", err)
			}
			return customErr
		},
	)
	if !handlerCalled {
		t.Fatal("custom error handler was not called")
	}
	if err != customErr {
		t.Fatalf("DeleteModelWithConn() error = %v, want custom error %v", err, customErr)
	}
}
