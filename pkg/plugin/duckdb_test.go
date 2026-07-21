package plugin

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newLocalEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(context.Background(), "", "")
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestWrapWithLimit(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "SELECT 1", "SELECT * FROM (SELECT 1\n) AS __grafana_q LIMIT 11"},
		{"trailing semicolon", "SELECT 1;", "SELECT * FROM (SELECT 1\n) AS __grafana_q LIMIT 11"},
		{"trailing whitespace and semicolons", "SELECT 1 ;\n ;", "SELECT * FROM (SELECT 1\n) AS __grafana_q LIMIT 11"},
		{"trailing line comment", "SELECT 1 -- note", "SELECT * FROM (SELECT 1 -- note\n) AS __grafana_q LIMIT 11"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wrapWithLimit(c.in, 10); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestEngine_QueryLocalParquet(t *testing.T) {
	e := newLocalEngine(t)
	ctx := context.Background()
	fixture := filepath.ToSlash(filepath.Join(t.TempDir(), "fix.parquet"))
	if err := e.Exec(ctx, `COPY (
		SELECT TIMESTAMP '2026-01-01 00:00:00' + INTERVAL (i) MINUTE AS ts, i AS v
		FROM range(5) t(i)) TO '`+fixture+`' (FORMAT parquet)`); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	rows, err := e.Query(ctx, "SELECT ts, v FROM '"+fixture+"' ORDER BY ts", 1000)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if n != 5 {
		t.Fatalf("got %d rows, want 5", n)
	}
}

func TestEngine_RowCapApplied(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), "SELECT i FROM range(100) t(i)", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if n != 11 { // maxRows+1 so the caller can detect truncation
		t.Fatalf("got %d rows, want 11", n)
	}
}

func TestEngine_UTCSession(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), "SELECT current_setting('TimeZone')", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	rows.Next()
	var tz string
	if err := rows.Scan(&tz); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if tz != "UTC" {
		t.Fatalf("TimeZone = %q, want UTC", tz)
	}
}

func TestEngine_ContextCancellation(t *testing.T) {
	e := newLocalEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// Cross join large ranges: would take far longer than 100ms if not interrupted.
	_, err := e.Query(ctx, "SELECT count(*) FROM range(100000000) a, range(1000) b", 10)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
}

func TestEngine_LoadsAzureExtensionOffline(t *testing.T) {
	ext, err := FindExtension()
	if err != nil {
		t.Skipf("azure extension not present locally (run scripts/fetch-extensions.sh): %v", err)
	}
	e, err := NewEngine(context.Background(), ext, "")
	if err != nil {
		t.Skipf("azure extension not present locally (run scripts/fetch-extensions.sh): %v", err)
	}
	defer e.Close()
	rows, err := e.Query(context.Background(),
		"SELECT loaded FROM duckdb_extensions() WHERE extension_name='azure'", 10)
	if err != nil {
		t.Fatalf("query extensions: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("azure extension not listed")
	}
	var loaded bool
	if err := rows.Scan(&loaded); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !loaded {
		t.Fatal("azure extension listed but not loaded")
	}
}
