# Capacity Planning Operator

A Kubernetes operator that tracks PersistentVolumeClaim (PVC) usage trends and surfaces capacity risk through:

- `CapacityPlan` custom resources
- Prometheus metrics exposed by the operator
- optional `PrometheusRule` alert generation
- optional Grafana dashboard ConfigMap generation
- per-PVC natural language insights via configurable providers (OpenAI, Anthropic, FastAPI)

This project is built with Kubebuilder/controller-runtime.

## What It Does

The operator uses two cooperating controllers:

1. `PVCWatcherReconciler`
- Watches PVC events cluster-wide.
- Queries a metrics backend (`PVCMetricsClient`) for current usage (Prometheus client implementation provided).
- Stores usage samples in an in-memory per-PVC ring buffer.
- Updates operator Prometheus gauges for raw usage/capacity/sample count.
- On startup (when `--prometheus-url` is set), backfills existing PVC history from Prometheus before steady-state reconciliation.

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
- `llm`
  - LLM provider config (`disabled|openai|anthropic|fastapi`), model, timeout/maxTokens/temperature.
  - `llm.onlyAlertingPVCs` limits LLM refreshes to PVCs currently firing alerts.
  - OpenAI/Anthropic use Secret refs for API keys.
  - FastAPI supports in-cluster URL and optional bearer-token Secret.
- `budgets`
  - Optional forecast budgets by namespace/workload.
  - `budgets.namespaceBudgets[].budget` and `budgets.workloadBudgets[].budget` use Kubernetes quantity format (for example `20Ti`).
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

`status.summary` includes:

- total PVC count
- alerting PVC count
- top-N by highest usage ratio
- top-N by soonest predicted full
- top-N by highest positive growth
- readiness conditions (`Ready`, `PrometheusReady`, `LLMReady`, `BackfillReady`)

`status.topRisks[]` includes:

- top weekly growth PVCs
- projected full date (`projectedFullAt`)
- weekly vs daily growth and acceleration
- trend confidence (`confidenceR2`)
- inferred workload owner (`workloadKind/workloadName`)
- optional per-risk `llmInsight`

`status.riskDigest` provides a plan-level natural-language summary suitable for alert annotations.

`status.riskChanges[]` and `status.riskChangeSummary` report new/escalated/recovered risk transitions between reconciles.

`status.namespaceForecasts[]` and `status.workloadForecasts[]` provide storage budget forecasts:

- configured budget bytes
- current used bytes / ratio
- aggregate growth bytes/day
- optional `daysUntilBreach` and `projectedBreachAt`

`status.anomalies[]` and `status.anomalySummary` capture detected growth anomalies:

- `acceleration_spike`
- `trend_instability`
- `sudden_growth`

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
- `capacityplan_pvc_projected_full_timestamp_seconds{namespace,pvc}` (`-1` means not calculable)
- `capacityplan_pvc_growth_acceleration{namespace,pvc}` (daily-vs-weekly trend acceleration ratio)
- `capacityplan_pvc_risk_changes_total{type}` (`type` = `new|escalated|recovered`)
- `capacityplan_namespace_budget_days_to_breach{namespace}` (`-1` means not calculable)
- `capacityplan_workload_budget_days_to_breach{namespace,kind,workload}` (`-1` means not calculable)
- `capacityplan_pvc_anomaly{namespace,pvc,type}` (`type` = `acceleration_spike|trend_instability|sudden_growth`)
- `capacityplan_pvc_anomalies_total{type}`
- `capacityplan_pvc_samples_count{namespace,pvc}`
- `capacityplan_llm_requests_total{provider,model}`
- `capacityplan_llm_errors_total{provider,model}`
- `capacityplan_llm_latency_seconds{provider,model}`

## Alerts and Dashboard

### PrometheusRule

If `monitoring.coreos.com/v1 ServiceMonitor` is discoverable, the reconciler also upserts a `PrometheusRule` with rules like:

- `PVCUsageHigh`
- `PVCUsageCritical`
- `PVCFillingUpSoon`
- `PVCFillingUpCritical`
- `PVCGrowthAccelerationSpike`
- `PVCTrendInstability`
- `NamespaceBudgetBreachSoon`
- `WorkloadBudgetBreachSoon`

Each generated rule now includes a `description` annotation enriched with `status.riskDigest` context (top growth PVCs and projected full dates).

## Notifications

`CapacityPlanNotification` resources can send risk digests to:

- Slack webhook
- SMTP email

Features:

- on-change dedupe (`spec.onChangeOnly`)
- cooldown control (`spec.cooldown`)
- dry-run mode (`spec.dryRun`)

Example manifests:

- `config/samples/capacityplanning_v1_capacityplannotification.yaml`
- `config/samples/secret_notification_slack_webhook.yaml`
- `config/samples/secret_notification_smtp.yaml`

### Grafana Dashboard

The reconciler upserts a ConfigMap named `capacityplan-dashboard-<plan-name>` with label:

- `grafana.com/dashboard: "1"`

This supports Grafana sidecar-based dashboard auto-discovery.

## LLM Insights

LLM integration is abstracted by `internal/llm.InsightGenerator`.

`CapacityPlanReconciler` resolves provider config from `spec.llm` and instantiates the selected backend (`openai`, `anthropic`, or `fastapi`). Rate-limiting is enforced via `status.lastLLMTime` and `spec.llmInsightsInterval`.
If `spec.llm.onlyAlertingPVCs=true`, only PVCs with `alertFiring=true` trigger LLM refreshes.

Provider auth details:
- `openai`: secret in operator namespace (`spec.llm.openai.secretRefName`, key default `apiKey`)
- `anthropic`: secret in operator namespace (`spec.llm.anthropic.secretRefName`, key default `apiKey`)
- `fastapi`: optional bearer token secret (`spec.llm.fastapi.authSecretRefName`, key default `token`)

FastAPI resilience details:
- Supports `healthURL` (defaults to `/<host>/healthz`).
- Enters degraded mode after configurable consecutive failures.
- During degraded mode, insight calls short-circuit until cooldown expires.
- Recovery requires a successful health probe before traffic resumes.

## Logging

The operator uses controller-runtime/zap structured logging (JSON by default).

- `--debug=true` enables debug verbosity.
- `--zap-log-level=debug` (or numeric levels) is also supported.
- `--zap-encoder=json|console` and other standard zap flags are supported.

## Repository Layout

- `cmd/main.go`: manager bootstrap and controller wiring
- `api/v1/`: CRD Go types
- `internal/controller/`: reconcilers and envtest suite
- `internal/analysis/`: ring buffer + OLS growth logic
- `internal/metrics/`: metrics backend interface + Prometheus client
- `internal/llm/`: LLM interface, provider factory, OpenAI/Anthropic/FastAPI clients, stub/mock clients
- `internal/operator/`: Prometheus metric registration
- `config/`: kustomize, CRDs, RBAC, manager manifests

## Prerequisites

- Go `1.26+`
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

## CI Pipelines

GitHub Actions workflow: `.github/workflows/ci.yaml`

- `unit-tests` job runs `make test` on every push and pull request.

Manual k3s integration workflow: `.github/workflows/k3s-integration.yaml`

- Manual-dispatch only (expensive by design).
- Boots a k3s cluster (via k3d), installs kube-prometheus-stack (Prometheus + Alertmanager), and deploys this operator image.
- Creates 5 PVC-backed writer pods with distinct storage profiles:
  - steady linear growth
  - bursty growth
  - slow trickle growth
  - churn (write + periodic delete)
  - delayed growth (late start)
- Applies a CI `CapacityPlan`, observes growth over a configurable trend window, and validates:
  - growth trend signal and Prometheus raw PVC metrics
  - growth math cross-check (status vs Prometheus `deriv()`)
  - status reconciliation and conditions
  - generated `PrometheusRule` alerts
  - exported budget/anomaly metrics
  - alert pipeline end-to-end:
    - capacity alerts in Prometheus `ALERTS`
    - workload budget alerts for all CI workloads
    - capacity alerts in Alertmanager API (`/api/v2/alerts`)
  - final machine-readable validation report (`/tmp/cpo-ci-validation-report.json`)

Integration harness files:

- `hack/ci/k3s_integration.sh`
- `cmd/ci-verify` (Go verification CLI used by CI script)
- `hack/ci/kube-prom-values.yaml`
- `hack/ci/manifests/workloads/` (split PVC/pod manifests + kustomization)
- `hack/ci/manifests/capacityplan.yaml.tmpl`

CI artifacts:

- Validation report JSON is uploaded on every integration/nightly run.
- On failure, diagnostics bundle includes raw captures plus `summary.txt` (generated by `ci-verify summarize-diagnostics`).

Version pinning in workflows:

- GitHub actions are pinned by commit SHA.
- `k3d`, `k3s` image, `kubectl`, and `helm` are pinned to explicit versions in workflow env vars.
- `kube-prometheus-stack` chart version is pinned via `KUBE_PROM_STACK_CHART_VERSION`.

Nightly workflow: `.github/workflows/nightly-e2e.yaml`

- Currently manual-dispatch only (scheduled trigger intentionally disabled).
- Reuses k3s integration setup with Alertmanager webhook routing override.
- Deploys a lightweight in-cluster receiver and validates actual alert delivery path from Alertmanager.
- Uses:
  - `hack/ci/nightly_alert_delivery.sh`
  - `hack/ci/kube-prom-values-alerting.yaml`

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

Provider-specific examples:
- OpenAI: `config/samples/secret_openai_api_key.yaml`, `config/samples/capacityplanning_v1_capacityplan_openai.yaml`
- Anthropic: `config/samples/secret_anthropic_api_key.yaml`, `config/samples/capacityplanning_v1_capacityplan_anthropic.yaml`
- FastAPI (in-cluster): `config/samples/secret_fastapi_bearer_token.yaml`, `config/samples/capacityplanning_v1_capacityplan_fastapi.yaml`

## Codebase Review Notes (Current State)

These are the main operational caveats to keep in mind:

1. Only one `CapacityPlan` is active at a time.
- The controller selects the active plan deterministically (oldest creation timestamp, then name).
- Non-active plans are marked `Ready=False` (`Reason=NotActivePlan`) and do not drive watcher or LLM behavior.

2. Startup backfill state is process-scoped.
- Startup backfill is wired in `cmd/main.go` via `BackfillAllPVCs(...)` when `--prometheus-url` is configured.
- Default backfill window is retention-based (`720` samples at `5m` step by default).

3. There is leftover scaffold code for Sample resources.
- The repo still contains Sample API/controller artifacts used as scaffolding and compatibility (`api/v1/sample_types.go`, `internal/controller/sample_controller.go`, related CRDs/RBAC rules).

## Roadmap-Friendly Improvements

High-impact next steps for this operator:

1. Add stronger provider client cache eviction and retry/backoff policy tuning for large clusters.
2. Remove legacy `Sample` API/controller scaffold.
3. Add optional persistence backend for historical samples to survive pod restarts.

## License

MIT
