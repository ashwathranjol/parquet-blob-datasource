// Seed uploads a demo parquet dataset to Azurite for the docker compose demo.
// Usage: go run ./cmd/seed [blob-endpoint]  (default http://127.0.0.1:10000/devstoreaccount1)
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	_ "github.com/duckdb/duckdb-go/v2"
)

func main() {
	endpoint := "http://127.0.0.1:10000/devstoreaccount1"
	if len(os.Args) > 1 {
		endpoint = os.Args[1]
	}
	connStr := "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
		"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
		"BlobEndpoint=" + endpoint + ";"
	ctx := context.Background()

	db, err := sql.Open("duckdb", "")
	must(err)
	defer db.Close()
	_, err = db.ExecContext(ctx, `COPY (
		SELECT now() - INTERVAL (i) MINUTE AS ts,
		       'sensor-' || (i % 3) AS sensor_id,
		       20 + 5 * sin(i / 30.0) + random() AS temp
		FROM range(1440) t(i)
	) TO 'demo.parquet' (FORMAT parquet)`)
	must(err)

	client, err := azblob.NewClientFromConnectionString(connStr, nil)
	must(err)
	if _, err := client.CreateContainer(ctx, "telemetry", nil); err != nil {
		log.Printf("container exists? %v", err)
	}
	f, err := os.Open("demo.parquet")
	must(err)
	defer f.Close()
	_, err = client.UploadFile(ctx, "telemetry", "year=2026/demo.parquet", f, nil)
	must(err)
	os.Remove("demo.parquet")
	fmt.Println("seeded az://telemetry/year=2026/demo.parquet (last 24h, 3 sensors)")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
