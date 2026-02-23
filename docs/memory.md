# Project Memory

## Purpose
Persistent notes for decisions, constraints, and operating context that should survive across sessions.

## Current State
- Language/runtime baseline: Go 1.26
- Main resource: `CapacityPlan` (`capacityplanning.pbsladek.io/v1`)
- Core controllers:
  - `PVCWatcherReconciler`
  - `CapacityPlanReconciler`

## Key Decisions
- `CapacityPlan.spec.prometheusURL` and `spec.sampleRetention` are applied at reconcile time to the shared watcher.
- Grafana dashboard namespace defaults to operator namespace when not explicitly set.
- RBAC markers centralized in `internal/controller/rbac_markers.go` to keep `make manifests` output aligned.
- LLM insights are now provider-driven (`disabled|openai|anthropic|fastapi`) via `spec.llm`.
- API keys/tokens are read from Kubernetes Secrets in operator namespace.
- Exactly one active `CapacityPlan` is enforced (oldest creation timestamp, then name); non-active plans are marked `NotActivePlan`.
- LLM client instances are cached by resolved provider config + secret `resourceVersion`.
- LLM observability metrics are emitted (`requests_total`, `errors_total`, `latency_seconds`).
- Startup backfill is enabled for all existing PVCs when `--prometheus-url` is set.
- `CapacityPlan.status.summary` exposes compact top-N leaderboard and counts.
- FastAPI provider supports degraded mode with health probing and cooldown.
- LLM refresh can be scoped to alerting PVCs via `spec.llm.onlyAlertingPVCs`.
- Status conditions now include `PrometheusReady`, `LLMReady`, and `BackfillReady`.
- Logging defaults to structured zap JSON with optional `--debug` verbosity.
- Added plan-level risk intelligence: `status.topRisks`, `status.riskDigest`, projected-full and acceleration metrics, and enriched alert annotations.
- Added risk transition tracking (`status.riskChanges`, `status.riskChangeSummary`, hash-based change detection).
- Added workload owner correlation (PVC -> Pod -> owner workload) in top-risk summaries.
- Added `CapacityPlanNotification` CRD + reconciler for Slack/email risk digests with cooldown/on-change/dry-run controls.
- Added namespace/workload storage budget forecasting (`spec.budgets` -> status forecasts + breach metrics/alerts).
- Added PVC growth anomaly detection (`acceleration_spike`, `trend_instability`, `sudden_growth`) with status + metrics + alerts.

## Known Caveats
- Historical samples are still in-memory only; data resets on operator restart.
- Legacy Sample scaffold code still exists.

## Next Actions
- Consider cache eviction policy tuning for very large numbers of distinct provider configs.
- Remove legacy Sample scaffold once no longer needed.
- Add optional durable sample persistence backend.

## Changelog
- 2026-02-23: Created initial `docs/memory.md`.
- 2026-02-23: Added provider-based LLM integration (OpenAI, Anthropic, FastAPI) to replace production stub path.
- 2026-02-23: Added active-plan enforcement, LLM request metrics, and LLM client caching.
- 2026-02-23: Added startup PVC backfill, compact status leaderboard summary, and FastAPI degraded-mode health handling.
- 2026-02-23: Added alert-only LLM refresh, summary top-by-growth ranking, expanded readiness conditions, and structured/debug logging defaults.
- 2026-02-23: Added weekly risk ranking with projected fill dates and risk digest propagation into PrometheusRule annotations.
- 2026-02-23: Added risk-change detection, workload correlation, and external notification routing via CapacityPlanNotification.
- 2026-02-23: Added budget breach forecasting and anomaly detection surfaced in status, metrics, and generated alert rules.
