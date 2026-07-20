package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func TestLoadSettings(t *testing.T) {
	s := backend.DataSourceInstanceSettings{
		JSONData:                []byte(`{"accountName":"acct","maxRows":5000}`),
		DecryptedSecureJSONData: map[string]string{"connectionString": "secret-cs"},
	}
	got, err := LoadSettings(s)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if got.AccountName != "acct" || got.MaxRows != 5000 || got.ConnectionString != "secret-cs" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadSettings_Defaults(t *testing.T) {
	s := backend.DataSourceInstanceSettings{JSONData: []byte(`{}`)}
	got, err := LoadSettings(s)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if got.MaxRows != 1000000 {
		t.Fatalf("MaxRows default = %d, want 1000000", got.MaxRows)
	}
}

func TestLoadSettings_MissingConnectionString(t *testing.T) {
	s := backend.DataSourceInstanceSettings{JSONData: []byte(`{"accountName":"a"}`)}
	got, err := LoadSettings(s)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if got.ConnectionString != "" {
		t.Fatalf("want empty connection string, got %q", got.ConnectionString)
	}
}

func TestRedact(t *testing.T) {
	err := errors.New("dial failed: AccountKey=abc123 rejected")
	red := redact(err, "AccountKey=abc123", "")
	if strings.Contains(red.Error(), "abc123") {
		t.Fatalf("secret leaked: %v", red)
	}
	if !strings.Contains(red.Error(), "[redacted]") {
		t.Fatalf("expected [redacted] marker: %v", red)
	}
	if redact(nil, "x") != nil {
		t.Fatal("redact(nil) must be nil")
	}
}

// End-to-end through QueryData with a local parquet file — no Azure needed.
func TestQueryData_LocalParquet(t *testing.T) {
	ds := newTestDatasource(t)
	ctx := context.Background()
	dir := strings.ReplaceAll(t.TempDir(), `\`, "/")
	if err := ds.engine.Exec(ctx, `COPY (
		SELECT TIMESTAMP '2026-01-01 00:00:00' + INTERVAL (i) MINUTE AS ts,
		       'sensor-' || (i % 2) AS sensor_id,
		       CAST(i AS DOUBLE) AS val
		FROM range(10) t(i)) TO '`+dir+`/e2e.parquet' (FORMAT parquet)`); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	q := func(rawSQL, format string) backend.DataResponse {
		body, _ := json.Marshal(map[string]string{"rawSql": rawSQL, "format": format})
		resp, err := ds.QueryData(ctx, &backend.QueryDataRequest{
			Queries: []backend.DataQuery{{RefID: "A", JSON: body}},
		})
		if err != nil {
			t.Fatalf("QueryData: %v", err)
		}
		return resp.Responses["A"]
	}

	t.Run("table format", func(t *testing.T) {
		r := q("SELECT ts, sensor_id, val FROM '"+dir+"/e2e.parquet' ORDER BY ts", "table")
		if r.Error != nil {
			t.Fatalf("response error: %v", r.Error)
		}
		if len(r.Frames) != 1 || r.Frames[0].Rows() != 10 {
			t.Fatalf("unexpected frames: %+v", r.Frames)
		}
	})

	t.Run("timeseries format goes wide", func(t *testing.T) {
		r := q("SELECT ts, sensor_id, val FROM '"+dir+"/e2e.parquet' ORDER BY ts", "timeseries")
		if r.Error != nil {
			t.Fatalf("response error: %v", r.Error)
		}
		// long→wide: 1 time field + one value field per sensor_id
		if got := len(r.Frames[0].Fields); got != 3 {
			t.Fatalf("wide frame has %d fields, want 3 (time + 2 sensors)", got)
		}
	})

	t.Run("sql error passes through", func(t *testing.T) {
		r := q("SELECT nonexistent_col FROM '"+dir+"/e2e.parquet'", "table")
		if r.Error == nil || !strings.Contains(r.Error.Error(), "nonexistent_col") {
			t.Fatalf("want DuckDB error mentioning the column, got %v", r.Error)
		}
	})

	t.Run("row limit notice", func(t *testing.T) {
		ds.settings.MaxRows = 5
		defer func() { ds.settings.MaxRows = 1000000 }()
		r := q("SELECT ts FROM '"+dir+"/e2e.parquet'", "table")
		if r.Error != nil {
			t.Fatalf("response error: %v", r.Error)
		}
		f := r.Frames[0]
		if f.Rows() != 5 {
			t.Fatalf("got %d rows, want 5", f.Rows())
		}
		if f.Meta == nil || len(f.Meta.Notices) == 0 ||
			!strings.Contains(f.Meta.Notices[0].Text, "Row limit") {
			t.Fatalf("want row-limit notice, got meta %+v", f.Meta)
		}
	})
}

func newTestDatasource(t *testing.T) *Datasource {
	t.Helper()
	engine, err := NewEngine(context.Background(), "", "")
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return &Datasource{
		engine:   engine,
		settings: Settings{MaxRows: 1000000},
	}
}

// Azurite integration: CheckHealth + az:// query. Skips unless AZURITE_BLOB_ENDPOINT is set.
func TestAzuriteIntegration(t *testing.T) {
	endpoint := os.Getenv("AZURITE_BLOB_ENDPOINT") // e.g. http://127.0.0.1:10000/devstoreaccount1
	if endpoint == "" {
		t.Skip("AZURITE_BLOB_ENDPOINT not set")
	}
	connStr := "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
		"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
		"BlobEndpoint=" + endpoint + ";"
	ext, err := FindExtension()
	if err != nil {
		t.Skipf("azure extension missing: %v", err)
	}
	engine, err := NewEngine(context.Background(), ext, connStr)
	if err != nil {
		t.Fatalf("engine with azure: %v", err)
	}
	defer engine.Close()
	ds := &Datasource{engine: engine, settings: Settings{
		MaxRows: 1000000, ConnectionString: connStr, AccountName: "devstoreaccount1"}}

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if res.Status != backend.HealthStatusOk {
		t.Fatalf("health = %v: %s", res.Status, res.Message)
	}
}

func TestCheckHealth_BadKeyReportsAuthFailure(t *testing.T) {
	endpoint := os.Getenv("AZURITE_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("AZURITE_BLOB_ENDPOINT not set")
	}
	badConn := "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
		"AccountKey=" + strings.Repeat("A", 86) + "==;BlobEndpoint=" + endpoint + ";"
	ext, err := FindExtension()
	if err != nil {
		t.Skipf("azure extension missing: %v", err)
	}
	engine, err := NewEngine(context.Background(), ext, badConn)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer engine.Close()
	ds := &Datasource{engine: engine, settings: Settings{MaxRows: 10, ConnectionString: badConn}}
	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if res.Status != backend.HealthStatusError {
		t.Fatal("want error status for bad key")
	}
	if strings.Contains(res.Message, "AccountKey="+strings.Repeat("A", 8)) {
		t.Fatalf("secret leaked into health message: %s", res.Message)
	}
}
