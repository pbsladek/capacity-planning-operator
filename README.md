# Capacity Planning Operator

A Kubernetes operator that tracks PersistentVolumeClaim (PVC) usage trends and surfaces capacity risk through:

- `CapacityPlan` custom resources
- Prometheus metrics exposed by the operator
- optional `PrometheusRule` alert generation
- optional Grafana dashboard ConfigMap generation
- per-PVC natural language insights (currently via a stub LLM client)

This project is built with Kubebuilder/controller-runtime.

## What It Does

The operator uses two cooperating controllers:

1. `PVCWatcherReconciler`
- Watches PVC events cluster-wide.
- Queries a metrics backend (`PVCMetricsClient`) for current usage (Prometheus client implementation provided).
- Stores usage samples in an in-memory per-PVC ring buffer.
- Updates operator Prometheus gauges for raw usage/capacity/sample count.

2. `CapacityPlanReconciler`
- Reconciles `CapacityPlan` resources.
- Lists in-scope PVCs and reads sample snapshots from `PVCWatcherReconciler`.
- Runs OLS linear regression to compute growth (`bytes/day`), `daysUntilFull`, and `R^2` confidence.
- Updates `CapacityPlan.status.pvcs` with per-PVC summaries.
- Updates operator Prometheus gauges for growth and days-until-full.
- Optionally reconciles a `PrometheusRule` (if Prometheus Operator CRDs are present).
- Reconciles a Grafana dashboard `ConfigMap`.

Important design detail: the time-series samples are in-memory only. They are not persisted to etcd. Only summary results are written to `CapacityPlan.status`.

## API

- Group/version: `capacityplanning.pbsladek.io/v1`
- Kind: `CapacityPlan`
- Scope: `Cluster`
- Short name: `cp`

CRD type definitions live in `api/v1/capacityplan_types.go`.

### CapacityPlan Spec

- `namespaces []string`
  - Empty means all namespaces.
- `sampleRetention int`
  - Max samples kept per PVC in ring buffer (validation: `1..8760`, default in CRD: `720`).
- `reconcileInterval duration`
  - How often `CapacityPlanReconciler` requeues itself (default `1h`).
- `prometheusURL string`
  - Intended Prometheus base URL for PVC metrics.
- `thresholds.usageRatio string`
  - Alert threshold for used/capacity ratio (default `"0.85"`).
- `thresholds.daysUntilFull int`
  - Alert threshold in days (default `7`).
- `llmInsightsInterval duration`
  - Minimum time between LLM refreshes per PVC (default `6h`).
- `grafanaDashboardNamespace string`
  - Namespace where dashboard ConfigMap is written.

### CapacityPlan Status

`status.pvcs[]` includes, per PVC:

- capacity/usage bytes and usage ratio
- growth rate (`growthBytesPerDay`)
- predicted `daysUntilFull` (nullable)
- `confidenceR2`
- sample count + last sample timestamp
- `llmInsight` + `lastLLMTime`
- `alertFiring`

## Growth Algorithm

Growth is computed with ordinary least squares over `(time, usedBytes)` sample pairs:

- slope: bytes/day
- `R^2`: trend confidence
- `daysUntilFull`: computed only when slope > 0 and capacity is known

Implementation: `internal/analysis/growth.go`.

## Exported Metrics

Metrics are registered in `internal/operator/metrics.go` and served by controller-runtime metrics endpoint:

- `capacityplan_pvc_usage_bytes{namespace,pvc}`
- `capacityplan_pvc_capacity_bytes{namespace,pvc}`
- `capacityplan_pvc_usage_ratio{namespace,pvc}`
- `capacityplan_pvc_growth_bytes_per_day{namespace,pvc}`
- `capacityplan_pvc_days_until_full{namespace,pvc}` (`-1` means not calculable)
- `capacityplan_pvc_samples_count{namespace,pvc}`

## Alerts and Dashboard

### PrometheusRule

If `monitoring.coreos.com/v1 ServiceMonitor` is discoverable, the reconciler also upserts a `PrometheusRule` with rules like:

- `PVCUsageHigh`
- `PVCUsageCritical`
- `PVCFillingUpSoon`
- `PVCFillingUpCritical`

### Grafana Dashboard

The reconciler upserts a ConfigMap named `capacityplan-dashboard-<plan-name>` with label:

- `grafana.com/dashboard: "1"`

This supports Grafana sidecar-based dashboard auto-discovery.

## LLM Insights

LLM integration is abstracted by `internal/llm.InsightGenerator`.

Current wiring in `cmd/main.go` uses `StubInsightGenerator`, which returns deterministic placeholder text and performs no external API calls. Rate-limiting is enforced in `CapacityPlanReconciler` via `status.lastLLMTime` and `spec.llmInsightsInterval`.

## Repository Layout

- `cmd/main.go`: manager bootstrap and controller wiring
- `api/v1/`: CRD Go types
- `internal/controller/`: reconcilers and envtest suite
- `internal/analysis/`: ring buffer + OLS growth logic
- `internal/metrics/`: metrics backend interface + Prometheus client
- `internal/llm/`: LLM interface + stub/mock clients
- `internal/operator/`: Prometheus metric registration
- `config/`: kustomize, CRDs, RBAC, manager manifests

## Prerequisites

- Go `1.23+`
- Docker (for image build/push)
- Access to a Kubernetes cluster + `kubectl`

## Local Development

Common make targets:

```bash
make generate
make manifests
make build
make test
make run
```

If you run tests directly, ensure envtest binaries are available under `./bin/k8s` (the `make test` target handles setup).

## Build and Deploy

1. Build/push image:

```bash
make docker-build IMG=<registry>/<name>:<tag>
make docker-push IMG=<registry>/<name>:<tag>
```

2. Regenerate manifests from code markers:

```bash
make manifests
```

3. Install CRDs:

```bash
make install
```

4. Deploy controller:

```bash
make deploy IMG=<registry>/<name>:<tag>
```

## Example CapacityPlan

See `config/samples/capacityplanning_v1_capacityplan.yaml`.

Apply example:

```bash
kubectl apply -f config/samples/capacityplanning_v1_capacityplan.yaml
```

Inspect status:

```bash
kubectl get capacityplan cluster -o yaml
```

## Codebase Review Notes (Current State)

These are the main operational caveats to keep in mind:

1. The watcher is globally shared, while `CapacityPlan` is cluster-scoped and can be created multiple times.
- `spec.prometheusURL` and `spec.sampleRetention` are now applied by `CapacityPlanReconciler` to the shared watcher.
- If multiple plans use different values, the most recently reconciled plan becomes the active watcher configuration.

2. Historical backfill helper exists but is not invoked by startup flow.
- `PVCWatcherReconciler.BackfillFromRange(...)` is implemented but not currently called from `cmd/main.go`/controller wiring.

3. There is leftover scaffold code for Sample resources.
- The repo still contains Sample API/controller artifacts used as scaffolding and compatibility (`api/v1/sample_types.go`, `internal/controller/sample_controller.go`, related CRDs/RBAC rules).

## Roadmap-Friendly Improvements

High-impact next steps for this operator:

1. Define and enforce a strict policy for multiple `CapacityPlan` objects (currently last-reconciled plan wins for watcher config).
2. Add startup backfill invocation for tracked PVCs.
3. Replace stub LLM client with a real implementation (with retries, timeouts, and circuit breaking).
4. Remove legacy `Sample` API/controller scaffold.

## License

Apache-2.0
