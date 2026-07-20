#!/usr/bin/env bash
# Builds the linux/amd64 backend binary inside Docker so CGO works from the Windows dev box.
set -euo pipefail
docker run --rm -v "$(pwd):/plugin" -w /plugin -e CGO_ENABLED=1 golang:1.25 \
  go build -o dist/gpx_parquetblob_linux_amd64 ./pkg
