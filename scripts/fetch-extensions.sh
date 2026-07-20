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
