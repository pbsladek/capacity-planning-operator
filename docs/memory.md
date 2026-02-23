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

## Known Caveats
- Multiple `CapacityPlan` objects can conflict on shared watcher config (last reconciled wins).
- Historical backfill helper exists but is not wired into startup flow.
- Legacy Sample scaffold code still exists.

## Next Actions
- Decide/enforce policy for multiple `CapacityPlan` resources.
- Wire startup backfill flow.
- Remove legacy Sample scaffold once no longer needed.

## Changelog
- 2026-02-23: Created initial `docs/memory.md`.
- 2026-02-23: Added provider-based LLM integration (OpenAI, Anthropic, FastAPI) to replace production stub path.
