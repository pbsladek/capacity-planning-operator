#!/usr/bin/env bash
set -euo pipefail

bash hack/ci/run_ci_runner.sh integration "$@"
