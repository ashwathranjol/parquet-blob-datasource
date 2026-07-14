# Parquet Blob Datasource — Design

**Date:** 2026-07-13
**Status:** Approved for planning
**Author:** ashwathranjol (design assisted by Claude; all code authored by ashwathranjol)

## Summary

A Grafana **backend datasource plugin** that queries Apache Parquet files stored in
**Azure Blob Storage** using **embedded DuckDB** for full SQL support (JOINs, CTEs,
window functions, glob patterns, Hive partitioning). Fills a real gap: the existing
`tobiasworkstech-parquets3-datasource` covers only S3 and ships a deliberately
limited custom SQL executor; no maintained Azure Blob parquet datasource exists in
the Grafana catalog.

**Success criterion:** plugin published in the Grafana plugin catalog under the
author's account.

## Decisions Made

| Decision | Choice | Rationale |
|---|---|---|
| Storage backend | Azure Blob Storage only | Focused v1; genuine catalog gap; single SDK surface |
| Query engine | Embedded DuckDB (`go-duckdb`, CGO) | Full SQL is the differentiator vs. the S3 plugin's custom executor |
| Data access | DuckDB `azure` extension reads `az://` URLs directly (Approach A) | Ranged reads, predicate pushdown, glob + Hive partitioning for free; no file-fetching layer to write |
| Auth (v1) | Connection string / account key only, in `secureJsonData` | Simplest to build and test; other methods add cleanly later |
| Query UX (v1) | Raw SQL via Monaco editor + format dropdown | No visual builder in v1 |
| Template variables | In v1 | Nearly free alongside macro expansion |
| Plugin ID | `<grafana-username>-parquetblob-datasource` | Grafana signing requirement (username prefix) |

## Riskiest Assumption (spike first — Milestone 0)

DuckDB's `azure` extension must load **from a bundled local file** (no runtime
download from extensions.duckdb.org) inside a Go process via `go-duckdb`, on both
Windows (dev box) and Linux (production/CI), and must accept Azurite's development
connection string for local testing.

**Kill criterion:** if the extension cannot be bundled and loaded offline on
Windows and Linux, abandon Approach A and fall back to Approach B (Go `azblob`
SDK downloads files to a local cache; DuckDB queries local copies). The rest of
the design survives that fallback unchanged.

## Architecture

Standard `@grafana/create-plugin` two-halves layout:

```
parquet-blob-datasource/
├── src/                        # React/TS frontend (browser)
│   ├── components/
│   │   ├── ConfigEditor.tsx    # settings: account name + connection string
│   │   └── QueryEditor.tsx     # Monaco SQL editor + format dropdown
│   ├── datasource.ts           # DataSourceWithBackend subclass (thin)
│   ├── types.ts                # shared query/config types
│   └── plugin.json             # manifest: id, backend: true, executable
└── pkg/                        # Go backend (subprocess, gRPC via plugin SDK)
    ├── main.go
    └── plugin/
        ├── datasource.go       # instance lifecycle, QueryData, CheckHealth
        ├── duckdb.go           # engine wrapper: open, load ext, CREATE SECRET, exec
        ├── macros.go           # Grafana macro → DuckDB SQL rewriting (pure)
        └── frames.go           # DuckDB rows → Grafana data frames (pure)
```

### Components

1. **DuckDB engine wrapper** (`duckdb.go`) — one in-memory DuckDB per datasource
   instance. On startup: `LOAD` bundled azure extension, `CREATE SECRET` from the
   decrypted connection string. Interface: `Query(ctx, sql) → rows`. Enforces
   context deadline and row cap.
2. **Macro expander** (`macros.go`) — pure string function. Supports
   `$__timeFilter(col)`, `$__timeFrom`, `$__timeTo`. Timezone handling: all
   expansions emit UTC ISO-8601 literals.
3. **Frame converter** (`frames.go`) — pure function: DuckDB result columns →
   Grafana data frames. Explicit type map: TIMESTAMP/TIMESTAMPTZ → time,
   TINYINT..BIGINT/HUGEINT → int64 (HUGEINT values outside int64 range error),
   FLOAT/DOUBLE/DECIMAL → float64, VARCHAR → string, BOOLEAN → bool; all fields
   nullable. Unsupported types (nested LIST/STRUCT/MAP) produce a clear error
   naming the column, not a panic.
4. **ConfigEditor** (React) — storage account name (`jsonData`) + connection
   string (`secureJsonData`, write-only from the frontend).
5. **QueryEditor** (React) — Grafana `CodeEditor` (Monaco) with SQL language,
   plus format selector: Table | Time series.

## Data Flow

### Query path (per panel refresh)

1. Frontend interpolates dashboard template variables via `getTemplateSrv()`
   before the query leaves the browser.
2. Backend `QueryData` receives queries (rawSql, format) + time range.
3. Macro expansion (pure).
4. `duckdb.Query(ctx, sql)` — ctx carries Grafana's timeout; cancellation
   propagates to DuckDB interrupt.
5. Rows → frames. If format = Time series, apply the SDK long-to-wide conversion.

Example user query:

```sql
SELECT ts, sensor_id, avg(temp) AS temp
FROM 'az://telemetry/year=2026/**/*.parquet'
WHERE $__timeFilter(ts)
GROUP BY 1, 2 ORDER BY 1
```

### Config path (on datasource save)

`CheckHealth`: open DuckDB → load extension → `CREATE SECRET` → trivial probe
against the account. Failure messages distinguish: extension load failure vs.
auth rejection vs. container/network errors.

## Error Handling

- DuckDB SQL errors pass through verbatim to the panel query inspector.
- Max-rows guardrail (default 1,000,000, configurable in datasource options)
  applied as a defensive outer `LIMIT` wrapper; exceeding it returns a clear
  "row limit reached" notice on the frame.
- Query cancellation wired from gRPC context to DuckDB so abandoned panel
  refreshes stop blob egress.
- Connection strings redacted from all errors and logs (catalog review
  requirement).
- Extension load failures surface at instance creation / health check, never as
  confusing query-time errors.

## Testing

1. **Unit (pure Go):** macro expansion table tests; frame conversion tests
   covering every supported DuckDB type + the unsupported-type error path.
2. **Integration (DuckDB, local fixtures):** engine wrapper against `.parquet`
   files on disk — exercises CGO/DuckDB without Azure.
3. **Integration (Azurite):** azure extension against Azurite dev connection
   string in a Docker service container (validated by the spike). Full
   query-path tests with zero cloud cost.
4. **Manual E2E:** `docker compose up` = Grafana + Azurite + plugin (dev build)
   + provisioned datasource and dashboard. Doubles as README screenshot source.

## Build & Release

- CGO blocks easy cross-compilation. CI (GitHub Actions) builds each target on
  its native runner — linux/amd64, linux/arm64, windows/amd64, darwin/arm64 —
  then merges binaries + per-platform azure extension files into one plugin zip.
- Signing: Grafana Cloud access-policy token → `npx @grafana/sign-plugin` in CI.
- Pre-submission: `plugin-validator` must pass locally.
- Submission: zip URL via the grafana.com plugin submission form (community
  tier, free).

## Milestones

Each ends runnable; ashwathranjol writes the code, Claude guides.

| # | Deliverable | Proves |
|---|---|---|
| 0 | Spike: Go + DuckDB + bundled azure ext queries a real blob (Win + Linux) | The architecture bet; kill criterion above |
| 1 | `create-plugin` scaffold runs unmodified in local Grafana | Toolchain end-to-end |
| 2 | Config editor + CheckHealth green against real Azure | Auth path |
| 3 | Raw SQL → table panel renders | Hot path |
| 4 | Macros + time series format + template variables | Native feel |
| 5 | Frontend polish: Monaco, format picker, error display | UX complete |
| 6 | CI matrix build + signed zip + validator passes | Shippable |
| 7 | README, screenshots, provisioning docs, catalog submission | Published |

## Deliberate v1 Cuts (all reversible)

- Visual query builder
- SAS token / service principal / managed identity auth
- File/container browser UI
- Query result caching
- Alerting documentation (backend plugins get alerting automatically; we defer
  documenting guarantees, not the capability)
- GCS/S3 backends
