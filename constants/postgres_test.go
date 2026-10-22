package constants

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/ds/v4/pgds"
	"github.com/jackc/pgx/v5"
)

var postgresTestSequence atomic.Int64

type postgresTestDatabase struct {
	dsn        string
	conn       *pgx.Conn
	provider   ds.Provider
	repository *PostgresRepository
}

// The opt-in URL is used only to create a disposable database. Existing tables
// in that URL's database are never modified. The role needs CREATEDB permission.
func newPostgresTestDatabase(t *testing.T) postgresTestDatabase {
	t.Helper()
	dsn := os.Getenv("WEBAPP_CONSTANTS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WEBAPP_CONSTANTS_TEST_DATABASE_URL to run PostgreSQL integration tests (role needs CREATEDB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := fmt.Sprintf("webapp_constants_test_%d_%d", os.Getpid(), postgresTestSequence.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create disposable integration database: %v", err)
	}
	var conn *pgx.Conn
	var provider ds.Provider
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if provider != nil {
			_ = provider.Close()
		}
		if conn != nil {
			_ = conn.Close(cleanupCtx)
		}
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable integration database: %v", err)
		}
		_ = admin.Close(cleanupCtx)
	})
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.Database = databaseName
	// ConnString returns the original string, so reparse a URL-safe database
	// override using libpq's separately quoted dbname connection parameter.
	testDSN := dsn + ""
	if configURL, err := connectionStringWithDatabase(dsn, databaseName); err == nil {
		testDSN = configURL
	} else {
		t.Fatal(err)
	}
	conn, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, MigrationUp); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	provider, err = pgds.New(&pgds.Config{PrimaryConnStr: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewPostgresRepository(provider)
	if err != nil {
		t.Fatal(err)
	}
	return postgresTestDatabase{dsn: testDSN, conn: conn, provider: provider, repository: repository}
}

func seedPostgresConstant(t *testing.T, db postgresTestDatabase, code string, valueType ValueType, value string) Record {
	t.Helper()
	record, err := scanRecord(db.conn.QueryRow(context.Background(),
		"INSERT INTO public.webapp_constants (code, name, description, value_type, value) VALUES ($1, $1, 'Test setting', $2, $3::jsonb) RETURNING "+recordColumns,
		code, string(valueType), value).Scan)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPostgresRepositoryAtomicBatchAndVersions(t *testing.T) {
	db := newPostgresTestDatabase(t)
	ctx := context.Background()
	first := seedPostgresConstant(t, db, "first", TypeInt64, "10")
	second := seedPostgresConstant(t, db, "second", TypeBool, "true")
	all, err := db.repository.LoadAll(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("LoadAll: %v, %v", all, err)
	}
	missing, err := db.repository.Load(ctx, []int64{999999})
	if err != nil || len(missing) != 0 {
		t.Fatalf("Load missing IDs: %v, %v", missing, err)
	}
	writes := []Write{
		{ID: second.ID, Code: second.Code, ValueType: second.ValueType, Value: []byte("false"), ExpectedVersion: 1},
		{ID: first.ID, Code: first.Code, ValueType: first.ValueType, Value: []byte("20"), ExpectedVersion: 1},
	}
	updated, err := db.repository.Update(ctx, writes)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || updated[0].ID != second.ID || updated[1].ID != first.ID || updated[0].Version != 2 || updated[1].Version != 2 {
		t.Fatalf("wrong result order or versions: %+v", updated)
	}
	// A conflict in the second entry cannot leave the first entry updated.
	writes[0].ExpectedVersion = 2
	writes[0].Value = []byte("true")
	_, err = db.repository.Update(ctx, writes)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	loaded, err := db.repository.Load(ctx, []int64{second.ID})
	if err != nil || len(loaded) != 1 || string(loaded[0].Value) != "false" || loaded[0].Version != 2 {
		t.Fatalf("batch was partially committed: %v, %v", loaded, err)
	}
	// The database, not a caller-supplied counter, owns the version.
	if _, err := db.conn.Exec(ctx, "UPDATE public.webapp_constants SET version = 9000 WHERE id = $1", first.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = db.repository.Load(ctx, []int64{first.ID})
	if err != nil || len(loaded) != 1 || loaded[0].Version != 2 {
		t.Fatalf("version changed without a value change: %v, %v", loaded, err)
	}
	if _, err := db.conn.Exec(ctx, "UPDATE public.webapp_constants SET name = 'Renamed' WHERE id = $1", first.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = db.repository.Load(ctx, []int64{first.ID})
	if err != nil || loaded[0].Version != 3 || loaded[0].Name != "Renamed" {
		t.Fatalf("metadata did not increment version: %v, %v", loaded, err)
	}
}

func TestPostgresRepositoryConcurrentWriters(t *testing.T) {
	db := newPostgresTestDatabase(t)
	record := seedPostgresConstant(t, db, "concurrent", TypeInt64, "1")
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, value := range []string{"2", "3"} {
		go func(value string) {
			start.Wait()
			_, err := db.repository.Update(context.Background(), []Write{{
				ID: record.ID, Code: record.Code, ValueType: record.ValueType,
				Value: []byte(value), ExpectedVersion: record.Version,
			}})
			results <- err
		}(value)
	}
	start.Done()
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestPostgresValueConstraintsAndMigrationDown(t *testing.T) {
	db := newPostgresTestDatabase(t)
	ctx := context.Background()
	for _, test := range []struct {
		kind  ValueType
		value string
	}{
		{TypeInt64, "1.5"}, {TypeInt64, "1.0"}, {TypeInt64, "9223372036854775808"},
		{TypeBool, `"true"`}, {TypeString, "123"},
		{TypeDecimal, "1.25"}, {TypeDecimal, `"1e2"`},
		{TypeFloat64, "1e309"},
	} {
		_, err := db.conn.Exec(ctx, "INSERT INTO public.webapp_constants (code, name, value_type, value) VALUES ('invalid', 'Invalid', $1, $2::jsonb)", string(test.kind), test.value)
		if err == nil {
			t.Fatalf("database accepted %s value %s", test.kind, test.value)
		}
	}
	integer := seedPostgresConstant(t, db, "maximum", TypeInt64, "9223372036854775807")
	seedPostgresConstant(t, db, "decimal", TypeDecimal, `"12345678901234567890.001"`)
	if _, err := db.conn.Exec(ctx, "UPDATE public.webapp_constants SET code = 'changed' WHERE id = $1", integer.ID); err == nil {
		t.Fatal("database accepted a changed immutable code")
	}
	if _, err := db.conn.Exec(ctx, MigrationDown); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(ctx, MigrationUp); err != nil {
		t.Fatalf("reapply migration after down: %v", err)
	}
	all, err := db.repository.LoadAll(ctx)
	if err != nil || len(all) != 0 {
		t.Fatalf("recreated catalogue: %v, %v", all, err)
	}
}
