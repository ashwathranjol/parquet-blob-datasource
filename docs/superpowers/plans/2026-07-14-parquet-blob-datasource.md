# Parquet Blob Datasource Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Grafana backend datasource plugin that queries Apache Parquet files in Azure Blob Storage via embedded DuckDB, published in the Grafana plugin catalog.

**Architecture:** Standard `@grafana/create-plugin` two-halves layout — a thin React/TS frontend (Monaco SQL editor + config form) and a Go backend that runs one in-memory DuckDB per datasource instance. DuckDB's `azure` extension (bundled as a local file, no runtime download) reads `az://` URLs directly, giving ranged reads, predicate pushdown, globs, and Hive partitioning for free.

**Tech Stack:** Go + `github.com/duckdb/duckdb-go/v2` (CGO), `github.com/grafana/grafana-plugin-sdk-go`, DuckDB `azure` extension, React/TypeScript with `@grafana/ui`, Azurite for local/integration testing, GitHub Actions native-runner matrix for release builds.

## Global Constraints

- **Plugin ID:** `ashwathranjol-parquetblob-datasource` — the prefix MUST equal the author's grafana.com username (Grafana signing requirement). Confirm the username at grafana.com before Task 2; if it differs from `ashwathranjol`, substitute the real one everywhere this plan says `ashwathranjol`.
- **Go driver:** `github.com/duckdb/duckdb-go/v2` v2.10504.0, which bundles **DuckDB v1.5.4** (verified 2026-07: the project moved from `marcboeker/go-duckdb` to `duckdb/duckdb-go` at v2.5.0; since DuckDB 1.5.0 the middle semver component encodes the DuckDB version — v2.10504.x ⇔ DuckDB 1.5.4).
- **Extension/DuckDB version lock:** DuckDB extensions only load into the exact DuckDB version they were built for. Every `azure.duckdb_extension` file fetched anywhere in this plan comes from `http://extensions.duckdb.org/v1.5.4/<platform>/azure.duckdb_extension.gz`. If the Go driver is ever bumped, all extension files must be re-fetched for the new version.
- **DuckDB platform names:** `linux_amd64`, `linux_arm64`, `osx_amd64`, `osx_arm64`. Runtime mapping from `runtime.GOOS`/`GOARCH` lives in `pkg/plugin/duckdb.go`.
- **Windows is NOT a shipped target** (decision 2026-07-19, Task 1 spike): duckdb-go static builds on Windows are DuckDB platform `windows_amd64_mingw`, and DuckDB publishes no `azure` extension for that platform (`http://extensions.duckdb.org/v1.5.4/windows_amd64_mingw/azure.duckdb_extension.gz` → 404; httpfs/json/parquet/icu exist, azure/aws do not). Target scenario is Grafana in Docker (Linux). A working Windows dev-only escape hatch (dynamic linking: `-tags=duckdb_use_lib` + official MSVC `duckdb.dll` + MSVC `windows_amd64` extension) is documented in `.superpowers/sdd/task-1-report.md` if ever needed.
- **CGO everywhere:** `CGO_ENABLED=1` for every backend build and test. No cross-compilation — Linux binaries are built inside Docker on the Windows dev box, and CI builds each target on its native runner. Do NOT use the scaffold's `mage build:*` targets (the SDK's mage build defaults to `CGO_ENABLED=0`); use the plain `go build` commands given in the tasks.
- **Windows dev box toolchain (verified 2026-07-19):** MSYS2 ucrt64 gcc 16 CANNOT link duckdb-go's prebuilt static libs (its libstdc++ dropped the `__emutls_v._ZSt11__once_call`/`__once_callable` symbols the libs reference). Use winlibs GCC 14.2.0 UCRT — the same toolchain DuckDB's own CI uses — installed at `C:\Users\ashwa\AppData\Local\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin` (prepend to PATH). Native Windows builds/tests work EXCEPT anything needing the azure extension (see previous bullet); azure-dependent tests run in Docker or CI.
- **Timezone:** every engine connection runs `SET TimeZone='UTC'`; all macro expansions emit UTC literals formatted `TIMESTAMP 'YYYY-MM-DD HH:MM:SS.mmm'`.
- **Row cap:** default 1,000,000 rows, overridable via datasource option `maxRows`; enforced as an outer `LIMIT maxRows+1` wrapper + truncation with a warning notice on the frame.
- **Secret hygiene:** the connection string lives only in `secureJsonData`; every error that could contain it passes through `redact()` before leaving the backend. Never log the connection string.
- **Azurite dev connection string** (public well-known dev credentials, safe to commit):
  `DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;`
- **Commit style:** conventional commits (`feat:`, `test:`, `chore:`, `ci:`, `docs:`), one commit per green step group as written in the tasks.

## File Structure (end state)

```
parquet-blob-datasource/
├── spike/                          # Task 1 throwaway proof (kept for reference)
│   ├── go.mod / main.go
├── src/                            # frontend
│   ├── components/ConfigEditor.tsx
│   ├── components/QueryEditor.tsx
│   ├── datasource.ts
│   ├── module.ts
│   ├── types.ts
│   └── plugin.json
├── pkg/                            # backend
│   ├── main.go
│   └── plugin/
│       ├── datasource.go / datasource_test.go   # lifecycle, QueryData, CheckHealth, settings, redact
│       ├── duckdb.go / duckdb_test.go           # engine wrapper, ext path resolution, LIMIT wrapper
│       ├── macros.go / macros_test.go           # pure macro expansion
│       └── frames.go / frames_test.go           # sql.Rows → data.Frame
├── scripts/fetch-extensions.sh     # downloads azure ext for all 5 platforms into duckdb_extensions/
├── cmd/seed/main.go                # uploads fixture parquet to Azurite (dev + CI reuse)
├── provisioning/                   # datasource + dashboard for docker compose E2E
├── docker-compose.yaml             # grafana + azurite
└── .github/workflows/ci.yml       # matrix build, sign, validate, package
```

---

### Task 1: Spike — DuckDB + bundled azure extension + Azurite (Milestone 0, KILL-CRITERION GATE)

This task decides the architecture. **Kill criterion:** if the azure extension cannot be loaded from a local file (offline) on both Windows and Linux, or rejects Azurite's connection string, STOP — report back, and the plan must be rewritten for Approach B (Go `azblob` SDK downloads files to local cache, DuckDB queries local copies).

**Files:**
- Create: `spike/go.mod`, `spike/main.go`, `spike/.gitignore`

**Interfaces:**
- Produces: confidence + the exact working `LOAD` / `CREATE SECRET` / `az://` incantations that `pkg/plugin/duckdb.go` (Task 4) copies. No code is reused directly.

- [ ] **Step 1: Init spike module and download the Windows extension**

Run (Git Bash, from repo root):

```bash
mkdir -p spike && cd spike
go mod init spike
go get github.com/duckdb/duckdb-go/v2@v2.10504.0
go get github.com/Azure/azure-sdk-for-go/sdk/storage/azblob@latest
mkdir -p ext/windows_amd64 ext/linux_amd64
curl -Lo ext/windows_amd64/azure.duckdb_extension.gz http://extensions.duckdb.org/v1.5.4/windows_amd64/azure.duckdb_extension.gz
gunzip ext/windows_amd64/azure.duckdb_extension.gz
curl -Lo ext/linux_amd64/azure.duckdb_extension.gz http://extensions.duckdb.org/v1.5.4/linux_amd64/azure.duckdb_extension.gz
gunzip ext/linux_amd64/azure.duckdb_extension.gz
```

Expected: two `azure.duckdb_extension` files, each several MB. If the URL 404s, check `go doc github.com/duckdb/duckdb-go/v2` / the module's release notes for the actual bundled DuckDB version and adjust the version segment.

Create `spike/.gitignore`:

```
ext/
*.parquet
```

- [ ] **Step 2: Start Azurite**

```bash
docker run -d --name azurite -p 10000:10000 mcr.microsoft.com/azure-storage/azurite azurite-blob --blobHost 0.0.0.0
```

Expected: container running; `curl -s http://127.0.0.1:10000/devstoreaccount1?comp=list` returns an XML error body (403/400 is fine — it proves the listener is up).

- [ ] **Step 3: Write the spike program**

`spike/main.go`:

```go
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
```

- [ ] **Step 4: Run on Windows**

```powershell
$env:PATH = "C:\msys64\ucrt64\bin;$env:PATH"   # gcc for CGO
cd spike
go run .
```

Expected: `SPIKE OK: 100 rows, ts range [2026-01-01 00:00:00 .. 2026-01-01 01:39:00]`. First build takes minutes (compiling DuckDB bindings).

If `LOAD` fails with an HTTP/TLS-flavored error at the query step (not the LOAD step), retry after adding `SET azure_transport_option_type = 'curl';` right after the LOAD — the extension docs call this out for proxy/SSL quirks. If that fixes it, record it: the engine wrapper in Task 4 must set it too.

- [ ] **Step 5: Run on Linux (Docker, host networking to reach Azurite)**

```bash
docker run --rm -v "$(pwd)/spike:/spike" -w /spike --add-host=host.docker.internal:host-gateway golang:1.24 \
  bash -c "sed 's|127.0.0.1|host.docker.internal|g' main.go > main_linux.go && mv main_linux.go main.go && go run ."
```

Note: the sed rewrites the Azurite endpoint for the container network and mutates the mounted file — run `git checkout -- spike/main.go` afterwards, or copy the dir first. Expected: same `SPIKE OK` line.

- [ ] **Step 6: Evaluate the kill criterion**

Both platforms printed `SPIKE OK` → Approach A confirmed, proceed. Anything else → STOP the plan and report which trigger failed with the verbatim error; the fallback is Approach B per the design doc.

- [ ] **Step 7: Commit**

```bash
git add spike/
git commit -m "feat: spike proving offline azure extension load + Azurite query (Milestone 0)"
```

---

### Task 2: Scaffold the plugin and prove the toolchain (Milestone 1)

**Files:**
- Create (generated): `src/**`, `pkg/**`, `package.json`, `go.mod`, `Magefile.go`, `docker-compose.yaml`, `.config/**`, `src/plugin.json`
- Create: `scripts/build-backend-linux.sh`

**Interfaces:**
- Produces: the module path in `go.mod` (all later Go imports use it — record it), the `executable` name from `src/plugin.json` (binary naming in Tasks 2, 8, 9), a running `docker compose` Grafana at `http://localhost:3000` with the unmodified scaffold plugin loaded.

- [ ] **Step 1: Confirm the grafana.com username**

Log in at https://grafana.com and read the username from the profile. It MUST be the plugin ID prefix. This plan assumes `ashwathranjol` — if it differs, use the real one in every following step.

- [ ] **Step 2: Run the scaffold**

From the repo root:

```powershell
npx @grafana/create-plugin@latest
```

Answer the prompts: plugin type **datasource**, add backend **yes**, plugin name **parquetblob**, organization **ashwathranjol**. It generates `./ashwathranjol-parquetblob-datasource/`.

- [ ] **Step 3: Move the scaffold to the repo root**

```powershell
Get-ChildItem -Force .\ashwathranjol-parquetblob-datasource | Move-Item -Destination . 
Remove-Item .\ashwathranjol-parquetblob-datasource
```

If the scaffold created its own `.git`, delete `ashwathranjol-parquetblob-datasource/.git` before moving. Merge any generated `.gitignore` with the existing one rather than overwriting.

- [ ] **Step 4: Record the two names later tasks depend on**

```powershell
Select-String -Path go.mod -Pattern "^module"
Select-String -Path src/plugin.json -Pattern "executable|\"id\""
```

Expected: module like `github.com/ashwathranjol/parquetblob`, id `ashwathranjol-parquetblob-datasource`, executable like `gpx_parquetblob`. **Write these down** — this plan writes imports as `github.com/ashwathranjol/parquetblob/pkg/plugin` and binaries as `gpx_parquetblob_<os>_<arch>`; substitute the recorded values if they differ.

- [ ] **Step 5: Frontend builds**

```powershell
npm install
npm run build
```

Expected: `dist/` populated, no errors.

- [ ] **Step 6: Backend builds for Linux via Docker (CGO — do NOT use mage)**

Create `scripts/build-backend-linux.sh`:

```bash
#!/usr/bin/env bash
# Builds the linux/amd64 backend binary inside Docker so CGO works from the Windows dev box.
set -euo pipefail
docker run --rm -v "$(pwd):/plugin" -w /plugin -e CGO_ENABLED=1 golang:1.25 \
  go build -o dist/gpx_parquetblob_linux_amd64 ./pkg
```

Run it (Git Bash): `bash scripts/build-backend-linux.sh`

Expected: `dist/gpx_parquetblob_linux_amd64` exists. First run is slow (module download + DuckDB compile — the scaffold's default backend has no DuckDB yet, so this one is actually fast; it gets slow from Task 4 on).

- [ ] **Step 7: Run Grafana and verify the plugin loads**

```powershell
docker compose up -d
curl -s http://localhost:3000/api/plugins/ashwathranjol-parquetblob-datasource
```

Expected: JSON describing the plugin (scaffold's dev compose enables anonymous admin). Also open http://localhost:3000 → Connections → Data sources → add — the plugin appears, its default health check returns green. This is Milestone 1 done.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat: create-plugin scaffold (datasource + backend) running in local Grafana (Milestone 1)"
```

---

### Task 3: Macro expansion — `pkg/plugin/macros.go` (pure, TDD)

**Files:**
- Create: `pkg/plugin/macros.go`
- Test: `pkg/plugin/macros_test.go`

**Interfaces:**
- Consumes: nothing (pure strings + `time.Time`).
- Produces: `func ExpandMacros(rawSQL string, from, to time.Time) string` — used by `datasource.go` (Task 6). Emits `TIMESTAMP 'YYYY-MM-DD HH:MM:SS.000'` UTC literals.

- [ ] **Step 1: Write the failing table test**

`pkg/plugin/macros_test.go`:

```go
package plugin

import (
	"testing"
	"time"
)

func TestExpandMacros(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 2, 12, 30, 45, 0, time.UTC)
	fromLit := "TIMESTAMP '2026-01-01 00:00:00.000'"
	toLit := "TIMESTAMP '2026-01-02 12:30:45.000'"

	cases := []struct {
		name, in, want string
	}{
		{"timeFilter",
			"SELECT * FROM t WHERE $__timeFilter(ts)",
			"SELECT * FROM t WHERE ts >= " + fromLit + " AND ts <= " + toLit},
		{"timeFilter with spaces",
			"WHERE $__timeFilter( ts )",
			"WHERE ts >= " + fromLit + " AND ts <= " + toLit},
		{"timeFilter qualified column",
			"WHERE $__timeFilter(e.created_at)",
			"WHERE e.created_at >= " + fromLit + " AND e.created_at <= " + toLit},
		{"timeFrom bare", "WHERE ts > $__timeFrom", "WHERE ts > " + fromLit},
		{"timeFrom parens", "WHERE ts > $__timeFrom()", "WHERE ts > " + fromLit},
		{"timeTo bare", "WHERE ts < $__timeTo", "WHERE ts < " + toLit},
		{"timeTo parens", "WHERE ts < $__timeTo()", "WHERE ts < " + toLit},
		{"multiple occurrences",
			"$__timeFilter(a) AND $__timeFilter(b)",
			"a >= " + fromLit + " AND a <= " + toLit + " AND b >= " + fromLit + " AND b <= " + toLit},
		{"no macros untouched",
			"SELECT 1 -- $__notAMacro",
			"SELECT 1 -- $__notAMacro"},
		{"non-UTC input normalized to UTC", // from/to converted below
			"$__timeFrom", "WHERE-INDEPENDENT"}, // replaced in the subtest
	}
	for _, c := range cases[:len(cases)-1] {
		t.Run(c.name, func(t *testing.T) {
			got := ExpandMacros(c.in, from, to)
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}

	t.Run("non-UTC input normalized to UTC", func(t *testing.T) {
		ist := time.FixedZone("IST", 5*3600+1800)
		got := ExpandMacros("$__timeFrom", from.In(ist), to)
		if got != fromLit {
			t.Fatalf("got %q want %q", got, fromLit)
		}
	})
}
```

- [ ] **Step 2: Run it — must fail to compile**

Run: `go test ./pkg/plugin/ -run TestExpandMacros -v`
Expected: FAIL — `undefined: ExpandMacros`.

- [ ] **Step 3: Implement**

`pkg/plugin/macros.go`:

```go
package plugin

import (
	"regexp"
	"strings"
	"time"
)

// Grafana macros supported in v1: $__timeFilter(col), $__timeFrom, $__timeTo
// (with or without trailing parens). All literals are UTC — the engine also
// runs with TimeZone='UTC', so naive TIMESTAMP literals compare correctly
// against both TIMESTAMP and TIMESTAMPTZ columns.

var (
	timeFilterRe = regexp.MustCompile(`\$__timeFilter\(\s*([^)]+?)\s*\)`)
	timeFromRe   = regexp.MustCompile(`\$__timeFrom(\(\))?`)
	timeToRe     = regexp.MustCompile(`\$__timeTo(\(\))?`)
)

func tsLiteral(t time.Time) string {
	return "TIMESTAMP '" + t.UTC().Format("2006-01-02 15:04:05.000") + "'"
}

func ExpandMacros(rawSQL string, from, to time.Time) string {
	fromLit, toLit := tsLiteral(from), tsLiteral(to)
	out := timeFilterRe.ReplaceAllStringFunc(rawSQL, func(m string) string {
		col := strings.TrimSpace(timeFilterRe.FindStringSubmatch(m)[1])
		return col + " >= " + fromLit + " AND " + col + " <= " + toLit
	})
	out = timeFromRe.ReplaceAllString(out, fromLit)
	out = timeToRe.ReplaceAllString(out, toLit)
	return out
}
```

(`ReplaceAllStringFunc` instead of `$1` templates: the replacement text contains no `$` today, but the func form stays correct if the literal format ever grows one.)

- [ ] **Step 4: Run tests — pass**

Run: `go test ./pkg/plugin/ -run TestExpandMacros -v`
Expected: PASS, all subtests.

- [ ] **Step 5: Commit**

```bash
git add pkg/plugin/macros.go pkg/plugin/macros_test.go
git commit -m "feat: Grafana time macro expansion for DuckDB SQL"
```

---

### Task 4: DuckDB engine wrapper — `pkg/plugin/duckdb.go` (integration-tested against local parquet, no Azure)

**Files:**
- Create: `pkg/plugin/duckdb.go`, `scripts/fetch-extensions.sh`
- Test: `pkg/plugin/duckdb_test.go`
- Modify: `go.mod` (adds duckdb-go), `.gitignore` (add `duckdb_extensions/`)

**Interfaces:**
- Consumes: nothing from earlier tasks (spike incantations only).
- Produces (used by Task 6):
  - `func NewEngine(ctx context.Context, extensionPath, connectionString string) (*Engine, error)` — empty `extensionPath` skips the LOAD (pure-local tests); empty `connectionString` skips CREATE SECRET.
  - `func (e *Engine) Query(ctx context.Context, sqlText string, maxRows int64) (*sql.Rows, error)` — applies the outer LIMIT wrapper (`maxRows+1`), propagates ctx cancellation.
  - `func (e *Engine) Exec(ctx context.Context, sqlText string) error` — used by CheckHealth probe and tests.
  - `func (e *Engine) Close() error`
  - `func FindExtension() (string, error)` — locates the bundled `azure.duckdb_extension` next to the executable, env override `PARQUETBLOB_AZURE_EXT`.
  - `func wrapWithLimit(sqlText string, maxRows int64) string` (package-private, unit-tested).

- [ ] **Step 1: Fetch extensions for local dev**

Create `scripts/fetch-extensions.sh`:

```bash
#!/usr/bin/env bash
# Downloads the DuckDB azure extension for every shipped platform.
# The version MUST match the DuckDB bundled by duckdb-go (see go.mod / plan Global Constraints).
set -euo pipefail
DUCKDB_VERSION="${DUCKDB_VERSION:-1.5.4}"
PLATFORMS=(linux_amd64 linux_arm64 osx_amd64 osx_arm64)  # no windows: azure ext unpublished for windows_amd64_mingw
for p in "${PLATFORMS[@]}"; do
  dir="duckdb_extensions/$p"
  mkdir -p "$dir"
  echo "fetching azure extension for $p (duckdb v$DUCKDB_VERSION)"
  curl -fsSL -o "$dir/azure.duckdb_extension.gz" \
    "http://extensions.duckdb.org/v${DUCKDB_VERSION}/${p}/azure.duckdb_extension.gz"
  gunzip -f "$dir/azure.duckdb_extension.gz"
done
```

Run: `bash scripts/fetch-extensions.sh`
Expected: four `duckdb_extensions/<platform>/azure.duckdb_extension` files. Add `duckdb_extensions/` to `.gitignore` (they're fetched, not committed).

- [ ] **Step 2: Add the driver dependency**

```powershell
$env:PATH = "C:\Users\ashwa\AppData\Local\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin;$env:PATH"
go get github.com/duckdb/duckdb-go/v2@v2.10504.0
```

- [ ] **Step 3: Write the failing tests**

`pkg/plugin/duckdb_test.go`:

```go
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
		{"plain", "SELECT 1", "SELECT * FROM (SELECT 1) AS __grafana_q LIMIT 11"},
		{"trailing semicolon", "SELECT 1;", "SELECT * FROM (SELECT 1) AS __grafana_q LIMIT 11"},
		{"trailing whitespace and semicolons", "SELECT 1 ;\n ;", "SELECT * FROM (SELECT 1) AS __grafana_q LIMIT 11"},
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
		t.Fatalf("NewEngine with extension: %v", err)
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
```

For `TestEngine_LoadsAzureExtensionOffline` to find the file, set the env override when running tests locally (see Step 5).

- [ ] **Step 4: Run tests — fail to compile**

Run: `go test ./pkg/plugin/ -run 'TestEngine|TestWrapWithLimit' -v`
Expected: FAIL — `undefined: NewEngine`, `undefined: wrapWithLimit`, `undefined: FindExtension`.

- [ ] **Step 5: Implement**

`pkg/plugin/duckdb.go`:

```go
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
```

- [ ] **Step 6: Run tests — pass**

```powershell
$env:PATH = "C:\Users\ashwa\AppData\Local\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin;$env:PATH"
go test ./pkg/plugin/ -run 'TestEngine|TestWrapWithLimit' -v
```

Expected: PASS, with `TestEngine_LoadsAzureExtensionOffline` SKIPPED natively on Windows (FindExtension errors for windows — no azure ext exists for the mingw platform; do NOT set `PARQUETBLOB_AZURE_EXT`). If the cancellation test flakes on timing, raise the range sizes, never sleep. Then run the extension-load test for real inside Docker (Git Bash, `MSYS_NO_PATHCONV=1` prefix needed):

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/w" -w /w -e CGO_ENABLED=1 \
  -e PARQUETBLOB_AZURE_EXT=/w/duckdb_extensions/linux_amd64/azure.duckdb_extension \
  golang:1.25 go test ./pkg/plugin/ -run 'TestEngine_LoadsAzureExtensionOffline' -v
```

Expected: PASS (not skipped).

- [ ] **Step 7: Commit**

```bash
git add pkg/plugin/duckdb.go pkg/plugin/duckdb_test.go scripts/fetch-extensions.sh .gitignore go.mod go.sum
git commit -m "feat: DuckDB engine wrapper with offline azure extension load and row-cap wrapper"
```

---

### Task 5: Frame conversion — `pkg/plugin/frames.go` (TDD against real DuckDB values)

Tests run real DuckDB `SELECT CAST(...)` queries through the Task 4 engine, so the Go types duckdb-go actually returns are pinned by execution, not by memory. If a `scan value type` error appears when running the tests, extend the switch in `convertValue` for the type the error names — the test tells you the truth.

**Files:**
- Create: `pkg/plugin/frames.go`
- Test: `pkg/plugin/frames_test.go`
- Modify: `go.mod` (adds grafana-plugin-sdk-go)

**Interfaces:**
- Consumes: `*sql.Rows` from `Engine.Query` (Task 4).
- Produces (used by Task 6): `func FrameFromRows(rows *sql.Rows, refID string, maxRows int64) (*data.Frame, bool, error)` — the bool is `truncated` (a row beyond maxRows existed and was dropped). All fields nullable. Unsupported types (LIST/STRUCT/MAP/UNION/BLOB/`[]`-suffixed) return an error naming the column.

- [ ] **Step 1: Add the SDK dependency**

```powershell
go get github.com/grafana/grafana-plugin-sdk-go@latest
```

- [ ] **Step 2: Write the failing tests**

`pkg/plugin/frames_test.go`:

```go
package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func frameFor(t *testing.T, query string) (interface{ Rows() int }, bool, error) {
	t.Helper()
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), query, 1000)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	return FrameFromRows(rows, "A", 1000)
}

func TestFrameFromRows_AllSupportedTypes(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), `SELECT
		TIMESTAMP '2026-01-01 10:20:30' AS c_ts,
		TIMESTAMPTZ '2026-01-01 10:20:30+00' AS c_tstz,
		CAST(1 AS TINYINT) AS c_i8,
		CAST(2 AS SMALLINT) AS c_i16,
		CAST(3 AS INTEGER) AS c_i32,
		CAST(4 AS BIGINT) AS c_i64,
		CAST(5 AS HUGEINT) AS c_i128,
		CAST(1.5 AS FLOAT) AS c_f32,
		CAST(2.5 AS DOUBLE) AS c_f64,
		CAST(3.25 AS DECIMAL(18,3)) AS c_dec,
		'hello' AS c_str,
		true AS c_bool`, 1000)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	frame, truncated, err := FrameFromRows(rows, "A", 1000)
	if err != nil {
		t.Fatalf("FrameFromRows: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if got := len(frame.Fields); got != 12 {
		t.Fatalf("got %d fields, want 12", got)
	}
	if frame.Rows() != 1 {
		t.Fatalf("got %d rows, want 1", frame.Rows())
	}
	// Spot-check representative conversions.
	ts := frame.Fields[0].At(0).(*time.Time)
	if !ts.Equal(time.Date(2026, 1, 1, 10, 20, 30, 0, time.UTC)) {
		t.Fatalf("timestamp = %v", ts)
	}
	if v := *frame.Fields[6].At(0).(*int64); v != 5 {
		t.Fatalf("hugeint = %d, want 5", v)
	}
	if v := *frame.Fields[9].At(0).(*float64); v != 3.25 {
		t.Fatalf("decimal = %v, want 3.25", v)
	}
	if v := *frame.Fields[10].At(0).(*string); v != "hello" {
		t.Fatalf("string = %q", v)
	}
	if v := *frame.Fields[11].At(0).(*bool); !v {
		t.Fatal("bool = false, want true")
	}
}

func TestFrameFromRows_Nulls(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(),
		`SELECT CAST(NULL AS BIGINT) AS a, CAST(NULL AS VARCHAR) AS b, CAST(NULL AS TIMESTAMP) AS c`, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	frame, _, err := FrameFromRows(rows, "A", 10)
	if err != nil {
		t.Fatalf("FrameFromRows: %v", err)
	}
	for i, f := range frame.Fields {
		if f.At(0) != nil {
			// nullable fields hold typed nil pointers; ensure they're nil
			switch v := f.At(0).(type) {
			case *int64:
				if v != nil {
					t.Fatalf("field %d not null", i)
				}
			case *string:
				if v != nil {
					t.Fatalf("field %d not null", i)
				}
			case *time.Time:
				if v != nil {
					t.Fatalf("field %d not null", i)
				}
			}
		}
	}
}

func TestFrameFromRows_HugeintOutOfRange(t *testing.T) {
	_, _, err := frameFor(t, "SELECT 170141183460469231731687303715884105727::HUGEINT AS big")
	if err == nil || !strings.Contains(err.Error(), "big") {
		t.Fatalf("want out-of-range error naming column 'big', got %v", err)
	}
}

func TestFrameFromRows_UnsupportedTypeNamesColumn(t *testing.T) {
	for _, q := range []string{
		"SELECT [1, 2, 3] AS my_list",
		"SELECT {'a': 1} AS my_struct",
		"SELECT map([1],[2]) AS my_map",
	} {
		_, _, err := frameFor(t, q)
		if err == nil || !strings.Contains(err.Error(), "my_") {
			t.Fatalf("query %q: want unsupported-type error naming the column, got %v", q, err)
		}
	}
}

func TestFrameFromRows_Truncation(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), "SELECT i FROM range(100) t(i)", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	frame, truncated, err := FrameFromRows(rows, "A", 10)
	if err != nil {
		t.Fatalf("FrameFromRows: %v", err)
	}
	if !truncated {
		t.Fatal("want truncated=true")
	}
	if frame.Rows() != 10 {
		t.Fatalf("got %d rows, want 10", frame.Rows())
	}
}
```

- [ ] **Step 3: Run tests — fail to compile**

Run: `go test ./pkg/plugin/ -run TestFrameFromRows -v`
Expected: FAIL — `undefined: FrameFromRows`.

- [ ] **Step 4: Implement**

`pkg/plugin/frames.go`:

```go
package plugin

import (
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// column kinds we can represent as Grafana fields (all nullable)
type colKind int

const (
	kindTime colKind = iota
	kindInt
	kindFloat
	kindString
	kindBool
)

// kindForDBType maps a DuckDB DatabaseTypeName to a field kind, or errors for
// nested/unsupported types. DECIMAL arrives parameterized, e.g. "DECIMAL(18,3)".
func kindForDBType(col, dbType string) (colKind, error) {
	t := strings.ToUpper(dbType)
	switch {
	case strings.HasSuffix(t, "[]"),
		strings.HasPrefix(t, "STRUCT"), strings.HasPrefix(t, "MAP"),
		strings.HasPrefix(t, "LIST"), strings.HasPrefix(t, "UNION"),
		strings.HasPrefix(t, "ARRAY"), t == "BLOB":
		return 0, fmt.Errorf("column %q has unsupported DuckDB type %s; cast it to a scalar type in your SQL", col, dbType)
	case strings.HasPrefix(t, "TIMESTAMP"), t == "DATETIME", t == "DATE":
		return kindTime, nil
	case t == "TINYINT", t == "SMALLINT", t == "INTEGER", t == "INT", t == "BIGINT",
		t == "HUGEINT", t == "UTINYINT", t == "USMALLINT", t == "UINTEGER":
		return kindInt, nil
	case t == "FLOAT", t == "REAL", t == "DOUBLE", strings.HasPrefix(t, "DECIMAL"):
		return kindFloat, nil
	case t == "VARCHAR", t == "UUID", strings.HasPrefix(t, "ENUM"):
		return kindString, nil
	case t == "BOOLEAN":
		return kindBool, nil
	default:
		return 0, fmt.Errorf("column %q has unsupported DuckDB type %s", col, dbType)
	}
}

func newField(name string, k colKind) *data.Field {
	switch k {
	case kindTime:
		return data.NewField(name, nil, []*time.Time{})
	case kindInt:
		return data.NewField(name, nil, []*int64{})
	case kindFloat:
		return data.NewField(name, nil, []*float64{})
	case kindBool:
		return data.NewField(name, nil, []*bool{})
	default:
		return data.NewField(name, nil, []*string{})
	}
}

// convertValue turns a scanned driver value into the pointer type of its field.
func convertValue(col string, k colKind, v any) (any, error) {
	if v == nil {
		switch k {
		case kindTime:
			return (*time.Time)(nil), nil
		case kindInt:
			return (*int64)(nil), nil
		case kindFloat:
			return (*float64)(nil), nil
		case kindBool:
			return (*bool)(nil), nil
		default:
			return (*string)(nil), nil
		}
	}
	switch k {
	case kindTime:
		if t, ok := v.(time.Time); ok {
			u := t.UTC()
			return &u, nil
		}
	case kindInt:
		switch n := v.(type) {
		case int8:
			i := int64(n)
			return &i, nil
		case int16:
			i := int64(n)
			return &i, nil
		case int32:
			i := int64(n)
			return &i, nil
		case int64:
			return &n, nil
		case uint8:
			i := int64(n)
			return &i, nil
		case uint16:
			i := int64(n)
			return &i, nil
		case uint32:
			i := int64(n)
			return &i, nil
		case *big.Int: // HUGEINT
			if !n.IsInt64() {
				return nil, fmt.Errorf("column %q: HUGEINT value %s outside int64 range", col, n.String())
			}
			i := n.Int64()
			return &i, nil
		}
	case kindFloat:
		switch f := v.(type) {
		case float32:
			g := float64(f)
			return &g, nil
		case float64:
			return &f, nil
		case duckdb.Decimal:
			g := f.Float64()
			return &g, nil
		}
	case kindString:
		if s, ok := v.(string); ok {
			return &s, nil
		}
		if b, ok := v.([]byte); ok {
			s := string(b)
			return &s, nil
		}
	case kindBool:
		if b, ok := v.(bool); ok {
			return &b, nil
		}
	}
	return nil, fmt.Errorf("column %q: unexpected scan value type %T", col, v)
}

// FrameFromRows converts DuckDB rows into a single Grafana frame, appending at
// most maxRows rows. The returned bool is true when at least one extra row
// existed (the engine queries with LIMIT maxRows+1 so this is detectable).
func FrameFromRows(rows *sql.Rows, refID string, maxRows int64) (*data.Frame, bool, error) {
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, false, fmt.Errorf("read column types: %w", err)
	}
	kinds := make([]colKind, len(colTypes))
	fields := make([]*data.Field, len(colTypes))
	for i, ct := range colTypes {
		k, err := kindForDBType(ct.Name(), ct.DatabaseTypeName())
		if err != nil {
			return nil, false, err
		}
		kinds[i] = k
		fields[i] = newField(ct.Name(), k)
	}
	frame := data.NewFrame(refID, fields...)
	frame.RefID = refID

	scan := make([]any, len(colTypes))
	scanPtrs := make([]any, len(colTypes))
	for i := range scan {
		scanPtrs[i] = &scan[i]
	}
	var count int64
	truncated := false
	for rows.Next() {
		if count == maxRows {
			truncated = true
			break
		}
		if err := rows.Scan(scanPtrs...); err != nil {
			return nil, false, fmt.Errorf("scan row %d: %w", count, err)
		}
		for i, v := range scan {
			cv, err := convertValue(colTypes[i].Name(), kinds[i], v)
			if err != nil {
				return nil, false, err
			}
			frame.Fields[i].Append(cv)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return frame, truncated, nil
}
```

- [ ] **Step 5: Run tests — pass**

Run: `go test ./pkg/plugin/ -run TestFrameFromRows -v`
Expected: PASS. If any test fails with `unexpected scan value type <T>`, that's duckdb-go returning a different Go type than assumed — add that exact type to the switch in `convertValue` and re-run (do not delete the test).

- [ ] **Step 6: Commit**

```bash
git add pkg/plugin/frames.go pkg/plugin/frames_test.go go.mod go.sum
git commit -m "feat: DuckDB rows to Grafana frame conversion with explicit type map"
```

---

### Task 6: Datasource lifecycle, QueryData, CheckHealth — `pkg/plugin/datasource.go` (Milestones 2+3+4 backend)

**Files:**
- Create: `pkg/plugin/datasource.go` (replace the scaffold-generated one entirely)
- Modify: `pkg/main.go` (replace scaffold body)
- Test: `pkg/plugin/datasource_test.go`

**Interfaces:**
- Consumes: `ExpandMacros` (Task 3), `NewEngine`/`Engine.Query`/`FindExtension` (Task 4), `FrameFromRows` (Task 5).
- Produces (used by Task 7 frontend / Task 8 provisioning):
  - JSON query model: `{ "rawSql": string, "format": "table" | "timeseries" }`
  - `jsonData`: `{ "accountName": string, "maxRows": number }`; `secureJsonData`: `{ "connectionString": string }`
  - `func NewDatasource(ctx context.Context, s backend.DataSourceInstanceSettings) (instancemgmt.Instance, error)`

- [ ] **Step 1: Write the failing unit tests (settings + redact + query-model plumbing)**

`pkg/plugin/datasource_test.go`:

```go
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
```

- [ ] **Step 2: Run tests — fail to compile**

Run: `go test ./pkg/plugin/ -run 'TestLoadSettings|TestRedact|TestQueryData' -v`
Expected: FAIL — `undefined: LoadSettings`, `undefined: redact`, `undefined: Datasource`.

- [ ] **Step 3: Implement `datasource.go`**

Replace the scaffold's `pkg/plugin/datasource.go` with:

```go
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type Settings struct {
	AccountName      string
	MaxRows          int64
	ConnectionString string
}

func LoadSettings(s backend.DataSourceInstanceSettings) (Settings, error) {
	var jd struct {
		AccountName string `json:"accountName"`
		MaxRows     int64  `json:"maxRows"`
	}
	if len(s.JSONData) > 0 {
		if err := json.Unmarshal(s.JSONData, &jd); err != nil {
			return Settings{}, fmt.Errorf("parse datasource jsonData: %w", err)
		}
	}
	out := Settings{
		AccountName:      jd.AccountName,
		MaxRows:          jd.MaxRows,
		ConnectionString: s.DecryptedSecureJSONData["connectionString"],
	}
	if out.MaxRows <= 0 {
		out.MaxRows = 1_000_000
	}
	return out, nil
}

// redact removes secrets from an error before it can reach logs or the panel.
func redact(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return errors.New(msg)
}

type Datasource struct {
	engine   *Engine
	settings Settings
}

var (
	_ backend.QueryDataHandler      = (*Datasource)(nil)
	_ backend.CheckHealthHandler    = (*Datasource)(nil)
	_ instancemgmt.InstanceDisposer = (*Datasource)(nil)
)

func NewDatasource(ctx context.Context, s backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	settings, err := LoadSettings(s)
	if err != nil {
		return nil, err
	}
	ext, err := FindExtension()
	if err != nil {
		return nil, fmt.Errorf("azure extension unavailable (plugin packaging problem): %w", err)
	}
	engine, err := NewEngine(ctx, ext, settings.ConnectionString)
	if err != nil {
		return nil, redact(err, settings.ConnectionString)
	}
	return &Datasource{engine: engine, settings: settings}, nil
}

func (d *Datasource) Dispose() {
	if d.engine != nil {
		d.engine.Close()
	}
}

type queryModel struct {
	RawSQL string `json:"rawSql"`
	Format string `json:"format"`
}

func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	resp := backend.NewQueryDataResponse()
	for _, q := range req.Queries {
		resp.Responses[q.RefID] = d.query(ctx, q)
	}
	return resp, nil
}

func (d *Datasource) query(ctx context.Context, q backend.DataQuery) backend.DataResponse {
	var qm queryModel
	if err := json.Unmarshal(q.JSON, &qm); err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest, "invalid query JSON: "+err.Error())
	}
	if strings.TrimSpace(qm.RawSQL) == "" {
		return backend.DataResponse{} // empty query, empty response
	}
	sqlText := ExpandMacros(qm.RawSQL, q.TimeRange.From, q.TimeRange.To)

	rows, err := d.engine.Query(ctx, sqlText, d.settings.MaxRows)
	if err != nil {
		// DuckDB SQL errors pass through verbatim (minus secrets) — they are
		// the most useful debugging signal the user has.
		return backend.DataResponse{Error: redact(err, d.settings.ConnectionString)}
	}
	defer rows.Close()

	frame, truncated, err := FrameFromRows(rows, q.RefID, d.settings.MaxRows)
	if err != nil {
		return backend.DataResponse{Error: redact(err, d.settings.ConnectionString)}
	}
	if truncated {
		if frame.Meta == nil {
			frame.SetMeta(&data.FrameMeta{})
		}
		frame.Meta.Notices = append(frame.Meta.Notices, data.Notice{
			Severity: data.NoticeSeverityWarning,
			Text: fmt.Sprintf("Row limit reached: showing first %d rows. Refine the query or raise maxRows in the datasource settings.",
				d.settings.MaxRows),
		})
	}

	if qm.Format == "timeseries" {
		wide, err := data.LongToWide(frame, nil)
		if err != nil {
			return backend.DataResponse{Error: fmt.Errorf(
				"time series format needs a sorted time column plus value columns (long format): %w", err)}
		}
		if frame.Meta != nil {
			wide.SetMeta(frame.Meta)
		}
		frame = wide
	}
	return backend.DataResponse{Frames: data.Frames{frame}}
}

func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	fail := func(msg string) (*backend.CheckHealthResult, error) {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: msg}, nil
	}
	if d.settings.ConnectionString == "" {
		return fail("No connection string configured. Set it in the datasource settings.")
	}
	// Probe a container that should not exist: a 404-class error proves auth
	// worked; a 403/auth-class error means the credentials were rejected.
	err := d.engine.Exec(ctx,
		"SELECT count(*) FROM glob('az://grafana-health-probe-nonexistent/*')")
	if err == nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusOk,
			Message: "Connected to Azure Blob Storage."}, nil
	}
	msg := redact(err, d.settings.ConnectionString).Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "404") || strings.Contains(lower, "not exist") ||
		strings.Contains(lower, "containernotfound") || strings.Contains(lower, "no files found"):
		return &backend.CheckHealthResult{Status: backend.HealthStatusOk,
			Message: "Connected to Azure Blob Storage (auth accepted)."}, nil
	case strings.Contains(lower, "403") || strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "authorization") || strings.Contains(lower, "signature"):
		return fail("Azure rejected the credentials. Check the connection string. Detail: " + msg)
	default:
		return fail("Could not reach the storage account (network/endpoint problem?). Detail: " + msg)
	}
}
```

- [ ] **Step 4: Replace `pkg/main.go`**

```go
package main

import (
	"os"

	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"

	"github.com/ashwathranjol/parquetblob/pkg/plugin"
)

func main() {
	if err := datasource.Manage("ashwathranjol-parquetblob-datasource",
		plugin.NewDatasource, datasource.ManageOpts{}); err != nil {
		log.DefaultLogger.Error(err.Error())
		os.Exit(1)
	}
}
```

(Use the module path recorded in Task 2 Step 4 for the import; keep whatever import path the scaffold's main.go already used.)

- [ ] **Step 5: Run unit tests — pass**

Run: `go test ./pkg/plugin/ -run 'TestLoadSettings|TestRedact|TestQueryData' -v`
Expected: PASS. `LongToWide` requires the long frame sorted by time with the time column first — the tests use `ORDER BY ts`, matching the guidance the error message gives users.

- [ ] **Step 6: Run the Azurite integration tests**

These need the azure extension, which doesn't exist for native Windows — run them inside Docker against the host's Azurite (Git Bash):

```bash
docker start azurite  # from Task 1, or docker run ... as in Task 1 Step 2
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/w" -w /w \
  --add-host=host.docker.internal:host-gateway -e CGO_ENABLED=1 \
  -e PARQUETBLOB_AZURE_EXT=/w/duckdb_extensions/linux_amd64/azure.duckdb_extension \
  -e AZURITE_BLOB_ENDPOINT=http://host.docker.internal:10000/devstoreaccount1 \
  golang:1.25 go test ./pkg/plugin/ -run 'TestAzurite|TestCheckHealth' -v
```

Expected: PASS. If the error-classification asserts fail, read the actual Azurite error strings from the failure output and adjust the `switch` markers in `CheckHealth` to match reality — the test pins the real strings.

- [ ] **Step 7: Commit**

```bash
git add pkg/plugin/datasource.go pkg/plugin/datasource_test.go pkg/main.go
git commit -m "feat: QueryData, CheckHealth, settings, and secret redaction"
```

---

### Task 7: Frontend — types, datasource class, ConfigEditor, QueryEditor (Milestones 2/3/5 frontend)

The scaffold already generated versions of all these files; this task rewrites their contents. Keep the scaffold's file names, exports, and `module.ts` wiring — only swap implementations. If a scaffold import differs (e.g. `DataQuery` coming from `@grafana/schema` vs `@grafana/data`), keep the scaffold's import source.

**Files:**
- Modify: `src/types.ts`, `src/datasource.ts`, `src/components/ConfigEditor.tsx`, `src/components/QueryEditor.tsx`, `src/plugin.json`
- Test: `src/components/QueryEditor.test.tsx`, `src/components/ConfigEditor.test.tsx`

**Interfaces:**
- Consumes: backend JSON contracts from Task 6 — query `{rawSql, format}`, `jsonData {accountName, maxRows}`, `secureJsonData {connectionString}`. Field names must match exactly.
- Produces: the complete UI. No later task consumes frontend exports.

- [ ] **Step 1: `src/types.ts`**

```ts
import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

export type QueryFormat = 'table' | 'timeseries';

export interface ParquetBlobQuery extends DataQuery {
  rawSql?: string;
  format?: QueryFormat;
}

export const DEFAULT_QUERY: Partial<ParquetBlobQuery> = {
  rawSql: '',
  format: 'table',
};

export interface ParquetBlobDataSourceOptions extends DataSourceJsonData {
  accountName?: string;
  maxRows?: number;
}

export interface ParquetBlobSecureJsonData {
  connectionString?: string;
}
```

- [ ] **Step 2: `src/datasource.ts` — thin backend subclass + template variable interpolation**

```ts
import { CoreApp, DataSourceInstanceSettings, ScopedVars } from '@grafana/data';
import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';
import { DEFAULT_QUERY, ParquetBlobDataSourceOptions, ParquetBlobQuery } from './types';

export class DataSource extends DataSourceWithBackend<ParquetBlobQuery, ParquetBlobDataSourceOptions> {
  constructor(instanceSettings: DataSourceInstanceSettings<ParquetBlobDataSourceOptions>) {
    super(instanceSettings);
  }

  getDefaultQuery(_: CoreApp): Partial<ParquetBlobQuery> {
    return DEFAULT_QUERY;
  }

  // Dashboard template variables are expanded in the browser, before the
  // query reaches the backend (the backend never sees $variables).
  applyTemplateVariables(query: ParquetBlobQuery, scopedVars: ScopedVars): ParquetBlobQuery {
    return {
      ...query,
      rawSql: getTemplateSrv().replace(query.rawSql ?? '', scopedVars),
    };
  }

  filterQuery(query: ParquetBlobQuery): boolean {
    return !!query.rawSql?.trim();
  }
}
```

- [ ] **Step 3: `src/components/ConfigEditor.tsx`**

```tsx
import React, { ChangeEvent } from 'react';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import { InlineField, Input, SecretInput } from '@grafana/ui';
import { ParquetBlobDataSourceOptions, ParquetBlobSecureJsonData } from '../types';

interface Props
  extends DataSourcePluginOptionsEditorProps<ParquetBlobDataSourceOptions, ParquetBlobSecureJsonData> {}

export function ConfigEditor(props: Props) {
  const { onOptionsChange, options } = props;
  const { jsonData, secureJsonFields, secureJsonData } = options;

  const onAccountNameChange = (e: ChangeEvent<HTMLInputElement>) =>
    onOptionsChange({ ...options, jsonData: { ...jsonData, accountName: e.target.value } });

  const onMaxRowsChange = (e: ChangeEvent<HTMLInputElement>) =>
    onOptionsChange({
      ...options,
      jsonData: { ...jsonData, maxRows: e.target.value ? Number(e.target.value) : undefined },
    });

  const onConnectionStringChange = (e: ChangeEvent<HTMLInputElement>) =>
    onOptionsChange({
      ...options,
      secureJsonData: { ...secureJsonData, connectionString: e.target.value },
    });

  const onResetConnectionString = () =>
    onOptionsChange({
      ...options,
      secureJsonFields: { ...secureJsonFields, connectionString: false },
      secureJsonData: { ...secureJsonData, connectionString: '' },
    });

  return (
    <>
      <InlineField label="Storage account" labelWidth={22} tooltip="Azure storage account name">
        <Input
          id="config-editor-account-name"
          value={jsonData.accountName ?? ''}
          onChange={onAccountNameChange}
          placeholder="mystorageaccount"
          width={40}
        />
      </InlineField>
      <InlineField
        label="Connection string"
        labelWidth={22}
        tooltip="Stored encrypted; never sent back to the browser"
      >
        <SecretInput
          id="config-editor-connection-string"
          isConfigured={!!secureJsonFields.connectionString}
          value={secureJsonData?.connectionString ?? ''}
          onChange={onConnectionStringChange}
          onReset={onResetConnectionString}
          placeholder="DefaultEndpointsProtocol=https;AccountName=...;AccountKey=..."
          width={40}
        />
      </InlineField>
      <InlineField label="Max rows" labelWidth={22} tooltip="Row cap per query (default 1,000,000)">
        <Input
          id="config-editor-max-rows"
          type="number"
          value={jsonData.maxRows ?? ''}
          onChange={onMaxRowsChange}
          placeholder="1000000"
          width={40}
        />
      </InlineField>
    </>
  );
}
```

- [ ] **Step 4: `src/components/QueryEditor.tsx` — Monaco SQL + format picker**

```tsx
import React from 'react';
import { QueryEditorProps, SelectableValue } from '@grafana/data';
import { CodeEditor, InlineField, RadioButtonGroup } from '@grafana/ui';
import { DataSource } from '../datasource';
import { ParquetBlobDataSourceOptions, ParquetBlobQuery, QueryFormat } from '../types';

type Props = QueryEditorProps<DataSource, ParquetBlobQuery, ParquetBlobDataSourceOptions>;

const FORMATS: Array<SelectableValue<QueryFormat>> = [
  { label: 'Table', value: 'table' },
  { label: 'Time series', value: 'timeseries' },
];

export function QueryEditor({ query, onChange, onRunQuery }: Props) {
  return (
    <div>
      <CodeEditor
        language="sql"
        height="200px"
        value={query.rawSql ?? ''}
        showMiniMap={false}
        showLineNumbers={true}
        onBlur={(rawSql) => onChange({ ...query, rawSql })}
        onSave={(rawSql) => {
          onChange({ ...query, rawSql });
          onRunQuery();
        }}
      />
      <InlineField label="Format" labelWidth={10}>
        <RadioButtonGroup
          options={FORMATS}
          value={query.format ?? 'table'}
          onChange={(format) => {
            onChange({ ...query, format });
            onRunQuery();
          }}
        />
      </InlineField>
    </div>
  );
}
```

- [ ] **Step 5: Component tests**

`src/components/QueryEditor.test.tsx`:

```tsx
import React from 'react';
import { render, screen } from '@testing-library/react';
import { QueryEditor } from './QueryEditor';

// CodeEditor pulls in Monaco, which jsdom can't run — stub it.
jest.mock('@grafana/ui', () => ({
  ...jest.requireActual('@grafana/ui'),
  CodeEditor: (props: { value: string }) => <textarea data-testid="code-editor" defaultValue={props.value} />,
}));

const props = {
  query: { refId: 'A', rawSql: 'SELECT 1', format: 'table' as const },
  onChange: jest.fn(),
  onRunQuery: jest.fn(),
  datasource: {} as never,
};

describe('QueryEditor', () => {
  it('renders the SQL editor with the query text and both formats', () => {
    render(<QueryEditor {...(props as never)} />);
    expect(screen.getByTestId('code-editor')).toHaveValue('SELECT 1');
    expect(screen.getByLabelText('Table')).toBeInTheDocument();
    expect(screen.getByLabelText('Time series')).toBeInTheDocument();
  });
});
```

`src/components/ConfigEditor.test.tsx`:

```tsx
import React from 'react';
import { render, screen } from '@testing-library/react';
import { ConfigEditor } from './ConfigEditor';

const options = {
  jsonData: { accountName: 'acct' },
  secureJsonFields: { connectionString: true },
  secureJsonData: {},
} as never;

describe('ConfigEditor', () => {
  it('renders account name and a configured secret field', () => {
    render(<ConfigEditor options={options} onOptionsChange={jest.fn()} {...({} as never)} />);
    expect(screen.getByDisplayValue('acct')).toBeInTheDocument();
    expect(screen.getByText(/Reset/i)).toBeInTheDocument(); // SecretInput configured state
  });
});
```

- [ ] **Step 6: Run frontend checks**

```powershell
npm run typecheck
npm run test:ci
```

Expected: both pass. If the scaffold's jest setup chokes on the `@grafana/ui` partial mock, mock only `CodeEditor` via a module-path mock instead — do not delete the test.

- [ ] **Step 7: Tidy `src/plugin.json` metadata**

Verify/set these fields (leave scaffold defaults elsewhere):

```json
{
  "id": "ashwathranjol-parquetblob-datasource",
  "name": "Parquet Blob (Azure)",
  "type": "datasource",
  "backend": true,
  "alerting": true,
  "executable": "gpx_parquetblob",
  "info": {
    "description": "Query Apache Parquet files in Azure Blob Storage with full DuckDB SQL",
    "keywords": ["parquet", "azure", "blob", "duckdb", "sql"]
  }
}
```

- [ ] **Step 8: Manual smoke in Grafana (Milestone 3 gate)**

```powershell
npm run build
bash scripts/build-backend-linux.sh
docker compose up -d --force-recreate
```

Copy the linux extension next to the binary for the container (compose mounts `dist/`):

```bash
mkdir -p dist/duckdb_extensions/linux_amd64
cp duckdb_extensions/linux_amd64/azure.duckdb_extension dist/duckdb_extensions/linux_amd64/
```

In http://localhost:3000: create the datasource with the Azurite connection string **but with `127.0.0.1` replaced by `host.docker.internal`** (Grafana runs in a container), Save & Test → green. New dashboard → panel → query `SELECT ts, sensor_id, temp FROM 'az://telemetry/year=2026/**/*.parquet' WHERE $__timeFilter(ts) ORDER BY ts` (data seeded by the Task 1 spike) → table renders; switch format to Time series → chart renders. If `host.docker.internal` doesn't resolve, add `extra_hosts: ["host.docker.internal:host-gateway"]` to the grafana service in `docker-compose.yaml`.

- [ ] **Step 9: Commit**

```bash
git add src/ 
git commit -m "feat: config editor, Monaco SQL query editor, and template variable support"
```

---

### Task 8: One-command E2E environment — compose + seed + provisioning (Manual E2E from the spec)

Goal: `docker compose up` alone yields Grafana + Azurite + provisioned datasource + a demo dashboard with data. This doubles as the README screenshot source.

**Files:**
- Create: `cmd/seed/main.go`, `provisioning/datasources/parquetblob.yaml`, `provisioning/dashboards/dashboards.yaml`, `provisioning/dashboards/demo.json`
- Modify: `docker-compose.yaml`

**Interfaces:**
- Consumes: query/config JSON contracts (Task 6), plugin ID and binary layout (Tasks 2/7).
- Produces: the demo environment used for README screenshots (Task 10).

- [ ] **Step 1: Seed program**

`cmd/seed/main.go` (module-local reuse of the spike logic; run on the host to fill Azurite):

```go
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
```

Note `go get github.com/Azure/azure-sdk-for-go/sdk/storage/azblob@v1.5.0` in the root module (pinned: v1.8.0 sends an API version Azurite rejects — Task 1 finding) (it was previously only in `spike/`).

- [ ] **Step 2: Add Azurite to `docker-compose.yaml` and wire provisioning**

Add to the scaffold's compose file (keep the existing grafana service, extend it):

```yaml
services:
  azurite:
    image: mcr.microsoft.com/azure-storage/azurite
    command: azurite-blob --blobHost 0.0.0.0
    ports:
      - "10000:10000"
  grafana:
    # ... existing scaffold config stays ...
    depends_on:
      - azurite
    volumes:
      # existing dist mount stays; add:
      - ./provisioning:/etc/grafana/provisioning
```

- [ ] **Step 3: Provision the datasource**

`provisioning/datasources/parquetblob.yaml`:

```yaml
apiVersion: 1
datasources:
  - name: Parquet Blob (Azurite demo)
    uid: parquetblob-azurite
    type: ashwathranjol-parquetblob-datasource
    access: proxy
    jsonData:
      accountName: devstoreaccount1
      maxRows: 1000000
    secureJsonData:
      # Azurite public dev credentials — not a real secret.
      connectionString: "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://azurite:10000/devstoreaccount1;"
```

(`azurite` host, not 127.0.0.1 — Grafana resolves it on the compose network.)

- [ ] **Step 4: Provision a demo dashboard**

`provisioning/dashboards/dashboards.yaml`:

```yaml
apiVersion: 1
providers:
  - name: parquetblob-demo
    type: file
    options:
      path: /etc/grafana/provisioning/dashboards
```

`provisioning/dashboards/demo.json` — one dashboard, two panels, both querying the provisioned datasource by name:

```json
{
  "title": "Parquet Blob Demo",
  "uid": "parquetblob-demo",
  "time": { "from": "now-24h", "to": "now" },
  "panels": [
    {
      "id": 1, "type": "timeseries", "title": "Sensor temperature",
      "gridPos": { "h": 12, "w": 16, "x": 0, "y": 0 },
      "datasource": { "type": "ashwathranjol-parquetblob-datasource", "uid": "parquetblob-azurite" },
      "targets": [{
        "refId": "A",
        "format": "timeseries",
        "rawSql": "SELECT ts, sensor_id, temp FROM 'az://telemetry/year=2026/**/*.parquet' WHERE $__timeFilter(ts) ORDER BY ts"
      }]
    },
    {
      "id": 2, "type": "table", "title": "Latest readings",
      "gridPos": { "h": 12, "w": 8, "x": 16, "y": 0 },
      "datasource": { "type": "ashwathranjol-parquetblob-datasource", "uid": "parquetblob-azurite" },
      "targets": [{
        "refId": "A",
        "format": "table",
        "rawSql": "SELECT ts, sensor_id, temp FROM 'az://telemetry/year=2026/**/*.parquet' WHERE $__timeFilter(ts) ORDER BY ts DESC LIMIT 20"
      }]
    }
  ],
  "schemaVersion": 39
}
```

The dashboard references the datasource by the explicit `uid: parquetblob-azurite` pinned in the provisioning yaml above — no UI lookup needed.

- [ ] **Step 5: Bring it up and verify (Milestone 4 gate)**

```powershell
npm run build
bash scripts/build-backend-linux.sh
docker compose up -d
go run ./cmd/seed
```

Open http://localhost:3000/d/parquetblob-demo — both panels render data. Test a template variable: dashboard settings → variable `sensor` (custom: `sensor-0,sensor-1,sensor-2`), edit panel SQL to add `AND sensor_id = '$sensor'`, confirm switching the dropdown refetches.

- [ ] **Step 6: Commit**

```bash
git add cmd/seed provisioning docker-compose.yaml go.mod go.sum
git commit -m "feat: docker compose E2E demo with Azurite, provisioned datasource and dashboard"
```

---

### Task 9: CI — native matrix build, package, sign, validate (Milestone 6)

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: binary name `gpx_parquetblob_<os>_<arch>` (Task 2), `scripts/fetch-extensions.sh` (Task 4), test env contracts `PARQUETBLOB_AZURE_EXT` / `AZURITE_BLOB_ENDPOINT` (Tasks 4/6).
- Produces: `ashwathranjol-parquetblob-datasource-<version>.zip` release artifact used for catalog submission (Task 10).

- [ ] **Step 1: Write the workflow**

`.github/workflows/ci.yml`:

```yaml
name: CI
on:
  push:
    branches: [main]
    tags: ['v*']
  pull_request:

env:
  DUCKDB_VERSION: "1.5.4"   # must match duckdb-go (go.mod) — see plan Global Constraints

jobs:
  test:
    runs-on: ubuntu-latest
    services:
      azurite:
        image: mcr.microsoft.com/azure-storage/azurite
        ports: ["10000:10000"]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.25" }
      - name: Fetch azure extension (linux_amd64 only)
        run: |
          mkdir -p duckdb_extensions/linux_amd64
          curl -fsSL "http://extensions.duckdb.org/v${DUCKDB_VERSION}/linux_amd64/azure.duckdb_extension.gz" \
            | gunzip > duckdb_extensions/linux_amd64/azure.duckdb_extension
      - name: Go tests (unit + Azurite integration)
        env:
          CGO_ENABLED: "1"
          PARQUETBLOB_AZURE_EXT: ${{ github.workspace }}/duckdb_extensions/linux_amd64/azure.duckdb_extension
          AZURITE_BLOB_ENDPOINT: http://127.0.0.1:10000/devstoreaccount1
        run: go test ./pkg/... -v
      - uses: actions/setup-node@v4
        with: { node-version: "22" }
      - run: npm ci
      - run: npm run typecheck
      - run: npm run test:ci
      - run: npm run lint

  build-backend:
    strategy:
      matrix:
        include:
          - { os: ubuntu-latest,     goos: linux,   goarch: amd64, duckdb_platform: linux_amd64,  ext: "" }
          - { os: ubuntu-24.04-arm,  goos: linux,   goarch: arm64, duckdb_platform: linux_arm64,  ext: "" }
          - { os: macos-latest,      goos: darwin,  goarch: arm64, duckdb_platform: osx_arm64,    ext: "" }
          # no windows: DuckDB publishes no azure extension for windows_amd64_mingw (Task 1 finding)
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.25" }
      - name: Build (native, CGO on)
        shell: bash
        env: { CGO_ENABLED: "1" }
        run: |
          go build -o out/gpx_parquetblob_${{ matrix.goos }}_${{ matrix.goarch }}${{ matrix.ext }} ./pkg
      - name: Fetch matching azure extension
        shell: bash
        run: |
          mkdir -p out/duckdb_extensions/${{ matrix.duckdb_platform }}
          curl -fsSL "http://extensions.duckdb.org/v${DUCKDB_VERSION}/${{ matrix.duckdb_platform }}/azure.duckdb_extension.gz" \
            | gunzip > out/duckdb_extensions/${{ matrix.duckdb_platform }}/azure.duckdb_extension
      - uses: actions/upload-artifact@v4
        with:
          name: backend-${{ matrix.goos }}-${{ matrix.goarch }}
          path: out/

  package:
    needs: [test, build-backend]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: "22" }
      - run: npm ci
      - run: npm run build
      - uses: actions/download-artifact@v4
        with:
          pattern: backend-*
          merge-multiple: true
          path: dist/
      - name: Restore executable bits
        run: chmod +x dist/gpx_parquetblob_*
      - name: Sign plugin
        if: startsWith(github.ref, 'refs/tags/v')
        env:
          GRAFANA_ACCESS_POLICY_TOKEN: ${{ secrets.GRAFANA_ACCESS_POLICY_TOKEN }}
        run: npx @grafana/sign-plugin@latest
      - name: Zip
        run: |
          mv dist ashwathranjol-parquetblob-datasource
          zip -r ashwathranjol-parquetblob-datasource.zip ashwathranjol-parquetblob-datasource
          mv ashwathranjol-parquetblob-datasource dist
      - name: Validate
        run: |
          docker run --pull=always -v "$PWD:/work" -w /work grafana/plugin-validator-cli \
            -sourceCodeUri file:///work /work/ashwathranjol-parquetblob-datasource.zip
      - uses: actions/upload-artifact@v4
        with:
          name: plugin-zip
          path: ashwathranjol-parquetblob-datasource.zip
      - name: Attach to release
        if: startsWith(github.ref, 'refs/tags/v')
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh release create "${GITHUB_REF_NAME}" ashwathranjol-parquetblob-datasource.zip --generate-notes || \
             gh release upload "${GITHUB_REF_NAME}" ashwathranjol-parquetblob-datasource.zip
```

Known risks to expect on the first run (fix from the actual logs, don't pre-engineer): the macOS runner may need `unzip`/nothing extra but DuckDB compile is slow (~5-10 min); `ubuntu-24.04-arm` is the GitHub-hosted arm64 runner label — if unavailable on the account, drop linux/arm64 from v1 and note it in the README; the validator may flag metadata gaps (logo, links) that Task 10 fills.

- [ ] **Step 2: Add the signing token secret**

In the GitHub repo settings → Secrets → Actions, add `GRAFANA_ACCESS_POLICY_TOKEN` (create the token at grafana.com → Access Policies, realm: your org, scope `plugins:write`). Unsigned builds still work for PRs — signing only runs on tags.

- [ ] **Step 3: Push and iterate until green**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: native matrix build, sign, validate, package"
git push origin main
gh run watch
```

Expected: all jobs green; `plugin-zip` artifact downloadable. Iterate on real failures; each fix is its own `ci:` commit.

- [ ] **Step 4: Tag a release candidate**

```bash
git tag v0.1.0 && git push origin v0.1.0
gh run watch
```

Expected: signed zip attached to the GitHub release. Download it, unzip, check `MANIFEST.txt` exists and `duckdb_extensions/` has the three shipped platform dirs (linux_amd64, linux_arm64, osx_arm64).

---

### Task 10: README, screenshots, submission (Milestone 7)

**Files:**
- Create/Modify: `README.md`, `CHANGELOG.md`, `src/img/` screenshots, `src/plugin.json` (info.links, screenshots)

**Interfaces:**
- Consumes: the running Task 8 demo (screenshot source), the Task 9 release zip URL.
- Produces: the catalog submission.

- [ ] **Step 1: Write the README**

Sections (replace scaffold boilerplate): what it is (one paragraph + hero screenshot); requirements (Grafana ≥ scaffold's `grafanaDependency`, plugin is backend/CGO so only linux amd64/arm64 and darwin arm64 — no Windows: DuckDB ships no azure extension for the mingw platform the Go driver produces); configure (account name + connection string, screenshot of config editor); query (raw DuckDB SQL, `az://container/path/**/*.parquet` globs, Hive partitioning, JOINs/CTEs/window functions; macro table: `$__timeFilter(col)`, `$__timeFrom`, `$__timeTo`; formats table vs time series with the long-format requirement: time column + optional string label columns + numeric values, sorted by time); template variables example; row limit + `maxRows` option; local demo (`docker compose up` + `go run ./cmd/seed`); provisioning example (copy of Task 8 yaml with real-Azure placeholders); troubleshooting (health-check messages explained, extension-missing error, `azure_transport_option_type='curl'` note if the spike needed it); v1 limitations (from the design doc's cuts list); license.

- [ ] **Step 2: Screenshots**

From the Task 8 demo dashboard: `src/img/dashboard.png` (hero), `src/img/query-editor.png`, `src/img/config-editor.png`. Reference them in `src/plugin.json` under `info.screenshots` and set `info.links` to the GitHub repo + issues. Rebuild + re-run validator (Task 9 pipeline on a new tag, e.g. `v0.1.1`) to confirm the metadata checks pass.

- [ ] **Step 3: Update CHANGELOG.md**

```markdown
# Changelog

## 0.1.0
- Initial release: query Parquet files in Azure Blob Storage with full DuckDB SQL.
- Connection string auth, `$__timeFilter`/`$__timeFrom`/`$__timeTo` macros, table & time series formats, template variables, configurable row limit.
```

- [ ] **Step 4: Run the validator locally one final time**

```bash
docker run --pull=always -v "$PWD:/work" -w /work grafana/plugin-validator-cli \
  -sourceCodeUri file:///work /work/ashwathranjol-parquetblob-datasource.zip
```

Expected: no errors (warnings triaged: fix or consciously accept).

- [ ] **Step 5: Submit**

On grafana.com (logged in as the plugin-ID-prefix account): My Account → Plugins → Submit plugin. Provide the GitHub release zip URL and source repo URL, community tier. Respond to reviewer feedback as it arrives — turnaround is typically days-to-weeks; the design doc's success criterion (published in catalog) closes when the listing goes live.

- [ ] **Step 6: Commit**

```bash
git add README.md CHANGELOG.md src/img src/plugin.json
git commit -m "docs: README, screenshots, changelog for catalog submission"
git push
```

---

## Self-Review Notes

- **Spec coverage:** Milestone 0 → Task 1; M1 → Task 2; M2 → Tasks 6+7 (config/health); M3 → Tasks 4-7; M4 (macros, timeseries, variables) → Tasks 3, 6, 7 step 8, 8 step 5; M5 (Monaco/format/error UX) → Task 7; M6 → Task 9; M7 → Task 10. Error-handling section: verbatim SQL errors + redaction + row-limit notice + cancellation → Tasks 4/6 with tests. Testing section's four layers → Tasks 3/5 (unit), 4 (local DuckDB integration), 6 (Azurite integration), 8 (manual E2E).
- **Known deviations from spec, both deliberate:** (1) the spec's "one universal zip" ships extensions for 5 DuckDB platforms but binaries for 4 (no darwin/amd64 build — GitHub has no free Intel-mac runner; osx_amd64 extension still ships in case a binary is added later). (2) `frames.go` takes `*sql.Rows` rather than pre-fetched columns — "pure" in the sense of no I/O beyond the cursor, and it's what makes the row-cap/truncation single-pass.
- **Type consistency check:** `ExpandMacros(string, time.Time, time.Time) string` (T3) = usage in T6; `NewEngine(ctx, extensionPath, connectionString)` (T4) = usage in T6 tests and `NewDatasource`; `FrameFromRows(rows, refID, maxRows) (*data.Frame, bool, error)` (T5) = usage in T6; JSON contract `rawSql`/`format`/`accountName`/`maxRows`/`connectionString` identical across T6/T7/T8.
- **Version-lock reminder:** `go.mod` duckdb-go v2.10504.0 ⇔ DuckDB 1.5.4 ⇔ `DUCKDB_VERSION` in `scripts/fetch-extensions.sh` and `ci.yml`. Three places; grep for `1.5.4` when bumping.

