#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${CI_RUNNER_BIN:-}" && -x "${CI_RUNNER_BIN}" ]]; then
  exec "${CI_RUNNER_BIN}" "$@"
fi

if [[ -x "/tmp/cpo-ci-runner" ]]; then
  exec "/tmp/cpo-ci-runner" "$@"
fi

exec go run ./cmd/ci-runner "$@"
