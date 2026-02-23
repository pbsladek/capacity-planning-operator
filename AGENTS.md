# Agent Handoff Guide

This file is the restart protocol for any coding agent (Codex, Claude, or similar).

## Read Order (new thread)

1. Read `AGENTS.md` (this file).
2. Read `MEMORY.md` (current project state and next actions).
3. Run `git status --short` and confirm local changes before editing.

## Working Rules

1. Do not discard uncommitted user work.
2. Continue from `MEMORY.md` unless the user explicitly changes direction.
3. Keep diffs focused and reversible.
4. Validate changes with the smallest useful checks first, then broader checks.

## Update Rules (required)

After meaningful work, update `MEMORY.md`:

1. `Last updated` timestamp (UTC).
2. `Current objective`.
3. `What changed` (file list + short reason).
4. `Validation` (what was run and result).
5. `Next actions` (ordered, actionable).
6. `Blockers` (if any).

## CI Failure Triage Flow

1. Prefer artifact diagnostics over pasted logs.
2. Read `/tmp/cpo-ci-diagnostics/summary.txt` first.
3. Then inspect:
   - `prometheus/query_capacity_alerts.json`
   - `prometheus/query_capacity_metrics.json`
   - `prometheus/targets.json`
   - `monitoring/prometheusrules-all.yaml`
   - `monitoring/servicemonitors-all.yaml`
4. Patch root cause, not only the symptom.

## Repo Conventions

1. Keep docs in root: `AGENTS.md`, `MEMORY.md`, `CLAUDE.md`.
2. Put reusable CI helper scripts in `hack/ci/`.
3. Keep integration flow changes in:
   - `.github/workflows/k3s-integration.yaml`
   - `.github/workflows/nightly-e2e.yaml`
   - `hack/ci/k3s_integration.sh`
