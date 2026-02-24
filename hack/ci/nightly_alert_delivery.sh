#!/usr/bin/env bash
set -euo pipefail

bash hack/ci/run_ci_runner.sh nightly-alert-delivery "$@"
