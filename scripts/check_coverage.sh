#!/usr/bin/env bash
set -euo pipefail

threshold="${1:-35}"
profile="$(mktemp "${TMPDIR:-/tmp}/craken-cli-cover.XXXXXX")"
trap 'rm -f "$profile"' EXIT

go test ./... -coverprofile="$profile"
coverage="$(go tool cover -func="$profile" | awk '/^total:/ { sub(/%$/, "", $3); print $3 }')"

awk -v got="$coverage" -v want="$threshold" 'BEGIN {
  if (got + 0 < want + 0) {
    printf("coverage %.1f%% is below %.1f%%\n", got, want) > "/dev/stderr"
    exit 1
  }
  printf("coverage %.1f%% >= %.1f%%\n", got, want)
}'
