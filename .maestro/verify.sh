#!/usr/bin/env bash
set -euo pipefail

echo '=== Go vet ==='
go vet ./...

echo '=== Go test ==='
go test ./...

echo '=== Memory retrieval eval ==='
go run ./cmd/ok-gobot memory eval

echo '=== Build ok-gobot ==='
go build ./cmd/ok-gobot/

echo 'All verifications passed.'
