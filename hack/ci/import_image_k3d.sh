#!/usr/bin/env bash
set -euo pipefail

go run ./cmd/ci-runner import-image-k3d "$@"
