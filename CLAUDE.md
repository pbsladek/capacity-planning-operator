# capacity-planning-operator

## Thread Continuity (Read First)

For restarting work in a new agent thread:

1. Read `AGENTS.md` for handoff protocol.
2. Read `MEMORY.md` for latest project state, changed files, and next actions.
3. Run `git status --short` before making edits.

`CLAUDE.md` is reference documentation; `MEMORY.md` is the current working state.

A Kubernetes operator that watches PVC storage usage across all namespaces, tracks growth rates using
an in-memory ring buffer and OLS linear regression, exports Prometheus metrics, integrates optionally
with Alertmanager (PrometheusRule) and Grafana, and generates LLM-backed capacity planning insights.

## Module & Layout

- **Module**: `github.com/pbsladek/capacity-planning-operator`
- **Go version**: 1.26 (in go.mod; machine may have a newer version installed)
- **Framework**: controller-runtime v0.20.4 / kubebuilder v4 layout
- **Entry point**: `cmd/main.go`
- **Controllers**: `internal/controller/`
- **CRD types**: `api/v1/`
- **Config (CRD/RBAC/kustomize)**: `config/`
- **Local tool binaries**: `bin/` (controller-gen, kustomize, setup-envtest)
- **Test k8s binaries**: `bin/k8s/k8s/1.32.0-darwin-arm64/`

## API

- **Group**: `capacityplanning.pbsladek.io/v1`
- **CRD**: `CapacityPlan` — cluster-scoped, shortname `cp`
- **Types file**: `api/v1/capacityplan_types.go`
- **Sample CR**: `config/samples/capacityplanning_v1_capacityplan.yaml`
- **Old placeholder**: `api/v1/sample_types.go` (unused, safe to remove later)

## Architecture

Two controllers share in-memory state via `PVCWatcherReconciler.pvcStates`:

```
PVCWatcherReconciler          CapacityPlanReconciler
────────────────────          ──────────────────────
Triggered by: PVC events      Triggered by: CapacityPlan CR + RequeueAfter
Writes ring buffers           Reads ring buffers
Queries Prometheus HTTP API   Runs OLS growth analysis
Updates /metrics gauges       Calls LLM (rate-limited by LastLLMTime)
Cleans up deleted PVC state   Reconciles PrometheusRule + Grafana ConfigMap
                              Writes CapacityPlan status
```

**Shared state**: `map["namespace/name"]*PVCState` protected by `sync.RWMutex`. Ring buffer samples
are **never** written to etcd — only computed summaries (~1KB/PVC) go into `CapacityPlan.Status.PVCs`.

## Key Packages

| Package | Purpose |
|---------|---------|
| `internal/analysis` | `RingBuffer` (thread-safe circular buffer) + `CalculateGrowth` (OLS regression) |
| `internal/metrics` | `PVCMetricsClient` interface, Prometheus HTTP client, mock |
| `internal/llm` | `InsightGenerator` interface, provider factory (`openai`/`anthropic`/`fastapi`), stub + mock |
| `internal/operator` | Registers 6 Prometheus GaugeVecs in `init()` via controller-runtime registry |
| `internal/controller` | `PVCWatcherReconciler`, `CapacityPlanReconciler`, envtest suite |

## Prometheus Metrics

All exported on the operator's `/metrics` endpoint:

| Metric | Set by |
|--------|--------|
| `capacityplan_pvc_usage_bytes` | PVCWatcher |
| `capacityplan_pvc_capacity_bytes` | PVCWatcher |
| `capacityplan_pvc_usage_ratio` | PVCWatcher |
| `capacityplan_pvc_growth_bytes_per_day` | CapacityPlanReconciler |
| `capacityplan_pvc_days_until_full` | CapacityPlanReconciler (-1 if N/A) |
| `capacityplan_pvc_samples_count` | PVCWatcher |

## Make Targets

```bash
make generate    # Regenerate DeepCopy functions (run after changing api/v1/ types)
make manifests   # Regenerate CRD + RBAC YAMLs from kubebuilder markers
make build       # Compile binary to bin/manager
make test        # Run all unit + envtest tests
make install     # Install CRDs into the cluster at ~/.kube/config
make deploy IMG=<image>  # Deploy controller to cluster
```

> **Note**: `make manifests` uses `crd:allowDangerousTypes=true` because `PVCSummary` contains
> float64 fields (UsageRatio, GrowthBytesPerDay, ConfidenceR2).

## Runtime Flags

```
--prometheus-url string   Base URL of Prometheus (e.g. http://prometheus:9090).
                          If empty, PVC usage metrics are not collected.
--metrics-bind-address    Default "0" (disabled). Use :8080 for HTTP.
--metrics-secure          Default true (HTTPS). Use --metrics-secure=false for HTTP.
--leader-elect            Enable leader election (use in multi-replica deployments).
```

## Testing

- **Unit tests** (no envtest): `internal/analysis/` — 98.4% coverage
- **Envtest tests** (Ginkgo): `internal/controller/` — 72.4% coverage

### Envtest Gotchas

1. **PVC finalizers**: `kubernetes.io/pvc-protection` prevents deletion in envtest. Patch finalizers
   to `nil` before calling `Delete`, e.g.:
   ```go
   pvcPatch := pvc.DeepCopy()
   pvcPatch.Finalizers = nil
   Expect(k8sClient.Patch(ctx, pvcPatch, client.MergeFrom(pvc))).To(Succeed())
   Expect(k8sClient.Delete(ctx, pvcPatch)).To(Succeed())
   ```

2. **Injecting test samples**: The reconciler calls `ensureState` with the real PVC UID on every
   reconcile. To inject controlled samples, wait for the watcher to create state first, then lock
   the mutex and replace the buffer:
   ```go
   Eventually(func() int { return len(pvcWatcher.GetSnapshot(key)) }, "10s", "200ms").
       Should(BeNumerically(">=", 1))
   pvcWatcher.mu.Lock()
   state := pvcWatcher.pvcStates[key]
   state.Buffer = analysis.NewRingBuffer(10)
   // push samples...
   state.LastUID = pvc.UID
   pvcWatcher.mu.Unlock()
   ```

3. **LLM call counts**: Multiple PVCs in the cluster from previous tests will all trigger LLM calls.
   Test rate limiting by checking `LastLLMTime` stays unchanged, not by counting calls.

## Tool Versions

- `CONTROLLER_TOOLS_VERSION`: v0.18.0
- `KUSTOMIZE_VERSION`: v5.6.0
- `ENVTEST_VERSION`: release-0.20
- `ENVTEST_K8S_VERSION`: 1.32.0
