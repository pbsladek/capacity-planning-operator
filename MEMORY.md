# Project Memory

Last updated (UTC): 2026-02-23
Branch: `main`
Status: In progress

## Current Objective

Stabilize k3s integration/nightly alerting CI so capacity alerts are reliably evaluated and failures are easy to debug from one artifact.

## What Changed

1. Integration trend checks hardened in `hack/ci/k3s_integration.sh`.
   - Added bytes/min reporting and snapshot diagnostics.
   - Uses peak growing PVC count over observation window.
   - Non-zero usage detection now checks both `CapacityPlan.status` and raw Prometheus PVC usage.
   - Usage ratio sanity guard is disabled by default (`USAGE_RATIO_SANITY_MAX=0`) to avoid local-path false positives.

2. Workload pods kept alive longer in `hack/ci/manifests/workloads/pod-*.yaml`.
   - Post-write sleep changed to `1800` seconds to keep PVCs mounted during alert evaluation windows.

3. CRD install completeness fixed in `config/crd/kustomization.yaml`.
   - Includes both `capacityplans` and `capacityplannotifications`.

4. Prometheus scrape wiring added to default deployment.
   - `config/default/kustomization.yaml` includes `../prometheus`.
   - New files:
     - `config/prometheus/metrics_service.yaml`
     - `config/prometheus/service_monitor.yaml`
     - `config/prometheus/kustomization.yaml`

5. PrometheusRule selection labels added for kube-prometheus-stack.
   - `internal/controller/capacityplan_controller.go` now sets:
     - `release: kube-prometheus-stack`
     - `app.kubernetes.io/instance: kube-prometheus-stack`
   - Regression test added in `internal/controller/capacityplan_risk_test.go`.

6. Failure diagnostics workflow simplified.
   - New collector: `hack/ci/collect_diagnostics.sh`
   - Workflows now run collector and upload artifact on failure:
     - `.github/workflows/k3s-integration.yaml`
     - `.github/workflows/nightly-e2e.yaml`
   - Collector includes `summary.txt` with likely-cause hints.

7. Integration output now includes a clean pass report and faster trend observation behavior.
   - `hack/ci/k3s_integration.sh` adds:
     - final `Validation report` block with explicit pass statuses
     - early trend-observation stop when enough snapshots and growth signal are already confirmed
     - aggregate wait for all workload budget alerts instead of sequential timeout windows
   - New knobs:
     - `MIN_TREND_OBSERVE_SECONDS` (default `240`)
     - `MIN_TREND_SNAPSHOTS` (default `2`)
   - Workflow defaults reduced:
     - `TREND_OBSERVE_SECONDS=360`
     - `ALERT_PROPAGATION_TIMEOUT_SECONDS=600`

8. Capacity source in status calculations fixed.
   - `internal/controller/pvcwatcher_controller.go` stores last observed capacity per PVC in watcher state.
   - `internal/controller/capacityplan_controller.go` now prefers watcher-observed capacity when computing `usageRatio` and forecasts.
   - This aligns `CapacityPlan.status` ratios with Prometheus raw metrics instead of PVC request-size-only fallbacks.

9. Added growth-math verification gate in integration.
   - `hack/ci/k3s_integration.sh` now cross-checks `status.growthBytesPerDay` against an independent Prometheus `deriv(...) * 86400` calculation per PVC.
   - Configurable tolerances and minimum match thresholds:
     - `GROWTH_COMPARE_WINDOW_SECONDS`
     - `GROWTH_COMPARE_REL_TOL`
     - `GROWTH_COMPARE_ABS_TOL_BYTES_PER_DAY`
     - `MIN_GROWTH_COMPARABLE_PVCS`
     - `MIN_GROWTH_MATCHING_PVCS`
   - Result is included in final `Validation report` as `growth_math_crosscheck`.

## Validation Run

1. `bash -n hack/ci/k3s_integration.sh` passed.
2. `bash -n hack/ci/collect_diagnostics.sh` passed.
3. `go test ./internal/controller -run 'TestBuildPrometheusRuleUnstructured' -count=1` passed.
4. `go test ./internal/controller -run 'TestBuildPrometheusRuleUnstructured|TestBuildSummary|TestPVCWatcher' -count=1` passed.
5. Full `go test ./...` may fail locally without envtest binaries (`etcd`) configured.

## Open Items / Next Actions

1. Re-run GitHub Actions job and verify:
   - capacity alerts appear in Prometheus `ALERTS`
   - integration reaches Alertmanager verification
2. If failed, download diagnostics artifact and inspect `summary.txt` first.
3. Commit grouped changes once CI is green.

## High-Signal Files

1. `hack/ci/k3s_integration.sh`
2. `hack/ci/collect_diagnostics.sh`
3. `config/default/kustomization.yaml`
4. `config/prometheus/service_monitor.yaml`
5. `internal/controller/capacityplan_controller.go`
6. `.github/workflows/k3s-integration.yaml`
7. `.github/workflows/nightly-e2e.yaml`

## Quick Resume Commands

```bash
git status --short
bash -n hack/ci/k3s_integration.sh
bash -n hack/ci/collect_diagnostics.sh
go test ./internal/controller -run 'TestBuildPrometheusRuleUnstructured' -count=1
```
