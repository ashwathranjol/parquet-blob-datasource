package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
)

// Engine wraps one in-memory DuckDB per datasource instance.
type Engine struct {
	db *sql.DB
}

// NewEngine opens an in-memory DuckDB, pins the session to UTC, optionally
// loads the azure extension from a local file (never the network), and
// optionally registers the connection-string secret.
func NewEngine(ctx context.Context, extensionPath, connectionString string) (*Engine, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}
	e := &Engine{db: db}
	boot := []string{
		"SET autoinstall_known_extensions=false",
		"SET autoload_known_extensions=false",
		"SET TimeZone='UTC'",
	}
	for _, stmt := range boot {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("duckdb bootstrap %q: %w", stmt, err)
		}
	}
	if extensionPath != "" {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("LOAD '%s'", extensionPath)); err != nil {
			db.Close()
			return nil, fmt.Errorf("load azure extension from %s: %w", extensionPath, err)
		}
	}
	if connectionString != "" {
		stmt := fmt.Sprintf("CREATE SECRET grafana_azure (TYPE azure, CONNECTION_STRING '%s')",
			strings.ReplaceAll(connectionString, "'", "''"))
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			// Deliberately NOT wrapping the statement text: it contains the secret.
			return nil, fmt.Errorf("create azure secret: %w", err)
		}
	}
	return e, nil
}

func (e *Engine) Query(ctx context.Context, sqlText string, maxRows int64) (*sql.Rows, error) {
	return e.db.QueryContext(ctx, wrapWithLimit(sqlText, maxRows))
}

func (e *Engine) Exec(ctx context.Context, sqlText string) error {
	_, err := e.db.ExecContext(ctx, sqlText)
	return err
}

func (e *Engine) Close() error { return e.db.Close() }

// wrapWithLimit puts a defensive outer LIMIT of maxRows+1 around the user's
// query; the +1 row lets the frame converter detect truncation.
func wrapWithLimit(sqlText string, maxRows int64) string {
	trimmed := strings.TrimRight(strings.TrimSpace(sqlText), "; \t\n\r")
	trimmed = strings.TrimRight(strings.TrimSpace(trimmed), "; \t\n\r")
	return "SELECT * FROM (" + trimmed + ") AS __grafana_q LIMIT " + strconv.FormatInt(maxRows+1, 10)
}

// FindExtension resolves the bundled azure extension for this OS/arch.
// Layout in the shipped plugin: <executable dir>/duckdb_extensions/<platform>/azure.duckdb_extension
// Override with PARQUETBLOB_AZURE_EXT (used by tests and dev runs).
func FindExtension() (string, error) {
	if p := os.Getenv("PARQUETBLOB_AZURE_EXT"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	platform, err := duckdbPlatform()
	if err != nil {
		return "", err
	}
	p := filepath.Join(filepath.Dir(exe), "duckdb_extensions", platform, "azure.duckdb_extension")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("bundled azure extension not found at %s: %w", p, err)
	}
	return p, nil
}

func duckdbPlatform() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "linux_amd64", nil
	case "linux/arm64":
		return "linux_arm64", nil
	case "darwin/amd64":
		return "osx_amd64", nil
	case "darwin/arm64":
		return "osx_arm64", nil
	}
	// windows deliberately unsupported: duckdb-go static builds are platform
	// windows_amd64_mingw, for which no azure extension is published.
	return "", fmt.Errorf("unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
}
