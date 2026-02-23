#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${OUT_DIR:-/tmp/cpo-ci-diagnostics}"
OP_NS="${OP_NS:-k8s-operator-system}"
MON_NS="${MON_NS:-monitoring}"
PLAN_NAME="${PLAN_NAME:-ci-plan}"
PROM_PORT="${PROM_PORT:-19090}"
ALERT_PORT="${ALERT_PORT:-19093}"

mkdir -p "${OUT_DIR}"/{cluster,operator,monitoring,prometheus,alertmanager,logs,meta}

run_capture() {
  local outfile="$1"
  shift
  "$@" >"${outfile}" 2>&1 || true
}

json_query_has_results() {
  local file="$1"
  [[ -f "${file}" ]] || return 1
  ! grep -Eq '"result":[[:space:]]*\[[[:space:]]*\]' "${file}"
}

first_match_value() {
  local pattern="$1"
  local file="$2"
  [[ -f "${file}" ]] || return 0
  grep -m1 "${pattern}" "${file}" | sed -E "s/.*${pattern}[[:space:]]*//" || true
}

extract_condition_status() {
  local cond_type="$1"
  local file="$2"
  [[ -f "${file}" ]] || return 0
  awk -v t="${cond_type}" '
    $1=="type:" && $2==t { hit=1; next }
    hit && $1=="status:" { print $2; exit }
  ' "${file}" || true
}

summarize_diagnostics() {
  local summary_file="${OUT_DIR}/summary.txt"
  local cp_yaml="${OUT_DIR}/cluster/capacityplan-${PLAN_NAME}.yaml"
  local prom_alerts_q="${OUT_DIR}/prometheus/query_capacity_alerts.json"
  local prom_metrics_q="${OUT_DIR}/prometheus/query_capacity_metrics.json"
  local prom_up_q="${OUT_DIR}/prometheus/query_up_controller_manager.json"
  local prom_targets="${OUT_DIR}/prometheus/targets.json"
  local prom_rules="${OUT_DIR}/prometheus/rules.json"
  local all_rules="${OUT_DIR}/monitoring/prometheusrules-all.yaml"
  local all_sms="${OUT_DIR}/monitoring/servicemonitors-all.yaml"
  local op_sms="${OUT_DIR}/operator/servicemonitors.yaml"

  local ready_status prom_ready_status backfill_status last_reconcile
  ready_status="$(extract_condition_status "Ready" "${cp_yaml}")"
  prom_ready_status="$(extract_condition_status "PrometheusReady" "${cp_yaml}")"
  backfill_status="$(extract_condition_status "BackfillReady" "${cp_yaml}")"
  last_reconcile="$(first_match_value "lastReconcileTime:" "${cp_yaml}")"

  local target_up_count target_down_count cm_mentions
  target_up_count="$(grep -o '"health":"up"' "${prom_targets}" 2>/dev/null | wc -l | tr -d ' ')"
  target_down_count="$(grep -o '"health":"down"' "${prom_targets}" 2>/dev/null | wc -l | tr -d ' ')"
  cm_mentions="$(grep -o 'controller-manager' "${prom_targets}" 2>/dev/null | wc -l | tr -d ' ')"

  local capacity_alert_state capacity_metric_state controller_up_state
  if json_query_has_results "${prom_alerts_q}"; then
    capacity_alert_state="non-empty"
  else
    capacity_alert_state="empty"
  fi
  if json_query_has_results "${prom_metrics_q}"; then
    capacity_metric_state="non-empty"
  else
    capacity_metric_state="empty"
  fi
  if json_query_has_results "${prom_up_q}"; then
    controller_up_state="non-empty"
  else
    controller_up_state="empty"
  fi

  local rule_selected sm_selected
  if grep -q "name: capacityplan-${PLAN_NAME}" "${all_rules}" 2>/dev/null; then
    rule_selected="present"
  else
    rule_selected="missing"
  fi
  if grep -q "k8s-operator-controller-manager-metrics-monitor" "${all_sms}" 2>/dev/null; then
    sm_selected="present"
  elif grep -q "k8s-operator-controller-manager-metrics-monitor" "${op_sms}" 2>/dev/null; then
    sm_selected="present (operator namespace only)"
  else
    sm_selected="missing"
  fi

  {
    echo "Capacity Planning CI Diagnostics Summary"
    echo "GeneratedAtUTC: $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
    echo "PlanName: ${PLAN_NAME}"
    echo
    echo "[CapacityPlan]"
    echo "lastReconcileTime: ${last_reconcile:-unknown}"
    echo "Ready: ${ready_status:-unknown}"
    echo "PrometheusReady: ${prom_ready_status:-unknown}"
    echo "BackfillReady: ${backfill_status:-unknown}"
    echo
    echo "[Prometheus]"
    echo "targets.up: ${target_up_count:-0}"
    echo "targets.down: ${target_down_count:-0}"
    echo "targets.controller-manager-mentions: ${cm_mentions:-0}"
    echo "query.capacity_metrics: ${capacity_metric_state}"
    echo "query.controller_manager_up: ${controller_up_state}"
    echo "query.capacity_alerts: ${capacity_alert_state}"
    echo
    echo "[Resources]"
    echo "prometheusrule.capacityplan-${PLAN_NAME}: ${rule_selected}"
    echo "servicemonitor.controller-manager-metrics: ${sm_selected}"
    echo
    echo "[Likely Cause Hints]"
    if [[ "${capacity_metric_state}" == "empty" && "${controller_up_state}" == "empty" ]]; then
      echo "- Operator metrics are likely not being scraped by Prometheus (check ServiceMonitor selector/labels and targets)."
    fi
    if [[ "${capacity_metric_state}" == "non-empty" && "${capacity_alert_state}" == "empty" ]]; then
      echo "- Capacity metrics exist but ALERTS query is empty (check PrometheusRule selection labels, expressions, and 'for' duration windows)."
    fi
    if [[ "${rule_selected}" == "missing" ]]; then
      echo "- CapacityPlan PrometheusRule resource was not found (check controller reconcile logs for PrometheusRule create/update errors)."
    fi
    if [[ "${sm_selected}" == "missing" ]]; then
      echo "- Controller metrics ServiceMonitor was not found (Prometheus cannot discover controller metrics)."
    fi
  } >"${summary_file}"
}

capture_prometheus_api() {
  local pf_log="${OUT_DIR}/logs/prometheus-port-forward.log"
  kubectl -n "${MON_NS}" port-forward svc/kube-prometheus-stack-prometheus "${PROM_PORT}:9090" >"${pf_log}" 2>&1 &
  local pf_pid=$!
  sleep 2

  run_capture "${OUT_DIR}/prometheus/ready.txt" curl -fsS --max-time 10 "http://127.0.0.1:${PROM_PORT}/-/ready"
  run_capture "${OUT_DIR}/prometheus/alerts.json" curl -fsS --max-time 10 "http://127.0.0.1:${PROM_PORT}/api/v1/alerts"
  run_capture "${OUT_DIR}/prometheus/rules.json" curl -fsS --max-time 10 "http://127.0.0.1:${PROM_PORT}/api/v1/rules"
  run_capture "${OUT_DIR}/prometheus/targets.json" curl -fsS --max-time 10 "http://127.0.0.1:${PROM_PORT}/api/v1/targets"
  run_capture "${OUT_DIR}/prometheus/status-config.json" curl -fsS --max-time 10 "http://127.0.0.1:${PROM_PORT}/api/v1/status/config"
  run_capture "${OUT_DIR}/prometheus/query_capacity_alerts.json" \
    curl -fsS --max-time 10 --get \
      --data-urlencode 'query=ALERTS{alertname=~"PVCUsageHigh|PVCUsageCritical|NamespaceBudgetBreachSoon|WorkloadBudgetBreachSoon",alertstate=~"pending|firing"}' \
      "http://127.0.0.1:${PROM_PORT}/api/v1/query"
  run_capture "${OUT_DIR}/prometheus/query_up_controller_manager.json" \
    curl -fsS --max-time 10 --get \
      --data-urlencode 'query=up{job=~".*controller-manager.*"}' \
      "http://127.0.0.1:${PROM_PORT}/api/v1/query"
  run_capture "${OUT_DIR}/prometheus/query_capacity_metrics.json" \
    curl -fsS --max-time 10 --get \
      --data-urlencode 'query={__name__=~"capacityplan_.*"}' \
      "http://127.0.0.1:${PROM_PORT}/api/v1/query"

  kill "${pf_pid}" >/dev/null 2>&1 || true
  wait "${pf_pid}" >/dev/null 2>&1 || true
}

capture_alertmanager_api() {
  local pf_log="${OUT_DIR}/logs/alertmanager-port-forward.log"
  kubectl -n "${MON_NS}" port-forward svc/kube-prometheus-stack-alertmanager "${ALERT_PORT}:9093" >"${pf_log}" 2>&1 &
  local pf_pid=$!
  sleep 2

  run_capture "${OUT_DIR}/alertmanager/ready.txt" curl -fsS --max-time 10 "http://127.0.0.1:${ALERT_PORT}/-/ready"
  run_capture "${OUT_DIR}/alertmanager/status.json" curl -fsS --max-time 10 "http://127.0.0.1:${ALERT_PORT}/api/v2/status"
  run_capture "${OUT_DIR}/alertmanager/alerts.json" curl -fsS --max-time 10 "http://127.0.0.1:${ALERT_PORT}/api/v2/alerts"

  kill "${pf_pid}" >/dev/null 2>&1 || true
  wait "${pf_pid}" >/dev/null 2>&1 || true
}

run_capture "${OUT_DIR}/meta/timestamp.txt" date -u +"%Y-%m-%dT%H:%M:%SZ"
run_capture "${OUT_DIR}/meta/kubectl-version.txt" kubectl version --client=true

run_capture "${OUT_DIR}/cluster/nodes.txt" kubectl get nodes -o wide
run_capture "${OUT_DIR}/cluster/pods-all.txt" kubectl get pods -A -o wide
run_capture "${OUT_DIR}/cluster/events-all.txt" kubectl get events -A --sort-by=.lastTimestamp

run_capture "${OUT_DIR}/operator/resources.txt" kubectl -n "${OP_NS}" get deploy,rs,po,svc,ep
run_capture "${OUT_DIR}/operator/deploy.yaml" kubectl -n "${OP_NS}" get deploy -o yaml
run_capture "${OUT_DIR}/operator/pods.yaml" kubectl -n "${OP_NS}" get pods -o yaml
run_capture "${OUT_DIR}/operator/services.yaml" kubectl -n "${OP_NS}" get svc -o yaml
run_capture "${OUT_DIR}/operator/servicemonitors.yaml" kubectl -n "${OP_NS}" get servicemonitors.monitoring.coreos.com -o yaml
run_capture "${OUT_DIR}/operator/manager-logs.txt" kubectl -n "${OP_NS}" logs deploy/k8s-operator-controller-manager --tail=1200

run_capture "${OUT_DIR}/monitoring/resources.txt" kubectl -n "${MON_NS}" get deploy,sts,po,svc,prometheusrule,servicemonitor
run_capture "${OUT_DIR}/monitoring/prometheusrules.yaml" kubectl -n "${MON_NS}" get prometheusrules.monitoring.coreos.com -o yaml
run_capture "${OUT_DIR}/monitoring/servicemonitors.yaml" kubectl -n "${MON_NS}" get servicemonitors.monitoring.coreos.com -o yaml
run_capture "${OUT_DIR}/monitoring/prometheusrules-all.yaml" kubectl get prometheusrules.monitoring.coreos.com -A -o yaml
run_capture "${OUT_DIR}/monitoring/servicemonitors-all.yaml" kubectl get servicemonitors.monitoring.coreos.com -A -o yaml
run_capture "${OUT_DIR}/monitoring/operator-logs.txt" kubectl -n "${MON_NS}" logs deploy/kube-prometheus-stack-operator --tail=1200
run_capture "${OUT_DIR}/monitoring/prometheus-logs.txt" kubectl -n "${MON_NS}" logs statefulset/prometheus-kube-prometheus-stack-prometheus --tail=1200
run_capture "${OUT_DIR}/monitoring/alertmanager-logs.txt" kubectl -n "${MON_NS}" logs statefulset/alertmanager-kube-prometheus-stack-alertmanager --tail=1200

run_capture "${OUT_DIR}/cluster/capacityplans.txt" kubectl get capacityplans.capacityplanning.pbsladek.io -A
run_capture "${OUT_DIR}/cluster/capacityplan-${PLAN_NAME}.yaml" kubectl get capacityplan "${PLAN_NAME}" -o yaml

capture_prometheus_api
capture_alertmanager_api
summarize_diagnostics

echo "Diagnostics collected in ${OUT_DIR}"
