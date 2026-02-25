#!/usr/bin/env bash
set -euo pipefail

export CI_VALIDATION_SOFT_FAIL="${CI_VALIDATION_SOFT_FAIL:-true}"

bash hack/ci/run_ci_runner.sh integration "$@"
