package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	_ "github.com/duckdb/duckdb-go/v2"
)

const azuriteConn = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;"

func platform() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "windows_amd64"
	case "linux/amd64":
		return "linux_amd64"
	case "linux/arm64":
		return "linux_arm64"
	case "darwin/arm64":
		return "osx_arm64"
	case "darwin/amd64":
		return "osx_amd64"
	}
	log.Fatalf("unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
	return ""
}

func main() {
	ctx := context.Background()

	// 1. Generate a fixture parquet file using DuckDB itself (no extra deps).
	db, err := sql.Open("duckdb", "")
	must(err, "open duckdb")
	_, err = db.ExecContext(ctx, `COPY (
		SELECT TIMESTAMP '2026-01-01 00:00:00' + INTERVAL (i) MINUTE AS ts,
		       'sensor-' || (i % 3) AS sensor_id,
		       20.0 + random() * 5 AS temp
		FROM range(100) t(i)
	) TO 'fixture.parquet' (FORMAT parquet)`)
	must(err, "write fixture parquet")
	must(db.Close(), "close fixture db")

	// 2. Upload it to Azurite with the Go SDK.
	client, err := azblob.NewClientFromConnectionString(azuriteConn, nil)
	must(err, "azblob client")
	_, err = client.CreateContainer(ctx, "telemetry", nil)
	if err != nil {
		log.Printf("create container (may already exist): %v", err)
	}
	f, err := os.Open("fixture.parquet")
	must(err, "open fixture")
	_, err = client.UploadFile(ctx, "telemetry", "year=2026/data.parquet", f, nil)
	must(err, "upload blob")
	must(f.Close(), "close fixture")

	// 3. Fresh DuckDB: load the azure extension FROM A LOCAL FILE (offline path).
	db2, err := sql.Open("duckdb", "")
	must(err, "open duckdb 2")
	defer db2.Close()
	// Prove no network fallback: forbid autoinstall/autoload.
	_, err = db2.ExecContext(ctx, "SET autoinstall_known_extensions=false; SET autoload_known_extensions=false;")
	must(err, "disable autoinstall")
	extPath, err := filepath.Abs(filepath.Join("ext", platform(), "azure.duckdb_extension"))
	must(err, "ext path")
	_, err = db2.ExecContext(ctx, fmt.Sprintf("LOAD '%s'", extPath))
	must(err, "LOAD azure extension from local file") // <-- kill criterion trigger #1

	// 4. CREATE SECRET with the Azurite connection string.
	_, err = db2.ExecContext(ctx, fmt.Sprintf(
		"CREATE SECRET azurite (TYPE azure, CONNECTION_STRING '%s')", azuriteConn))
	must(err, "CREATE SECRET") // <-- kill criterion trigger #2

	// 5. Query the blob through az:// with a glob.
	row := db2.QueryRowContext(ctx,
		"SELECT count(*), min(ts), max(ts) FROM 'az://telemetry/year=2026/**/*.parquet'")
	var n int64
	var minTs, maxTs string
	must(row.Scan(&n, &minTs, &maxTs), "query az://") // <-- kill criterion trigger #3
	fmt.Printf("SPIKE OK: %d rows, ts range [%s .. %s]\n", n, minTs, maxTs)
}

func must(err error, what string) {
	if err != nil {
		log.Fatalf("SPIKE FAIL at %s: %v", what, err)
	}
}
