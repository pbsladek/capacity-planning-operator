#!/usr/bin/env bash
set -euo pipefail

OP_NS="${OP_NS:-k8s-operator-system}"
MON_NS="${MON_NS:-monitoring}"
CLUSTER_NAME="${CLUSTER_NAME:-cpo-ci}"
EXPECTED_KUBE_CONTEXT="${EXPECTED_KUBE_CONTEXT:-k3d-${CLUSTER_NAME}}"
PLAN_NAME="${PLAN_NAME:-ci-plan}"
OPERATOR_IMAGE="${OPERATOR_IMAGE:-capacity-planning-operator:ci}"
PROM_URL="${PROM_URL:-http://kube-prometheus-stack-prometheus.${MON_NS}.svc.cluster.local:9090}"
KUBE_PROM_VALUES_FILE="${KUBE_PROM_VALUES_FILE:-hack/ci/kube-prom-values.yaml}"
KUBE_PROM_VALUES_EXTRA_FILE="${KUBE_PROM_VALUES_EXTRA_FILE:-}"
KUBE_PROM_STACK_CHART_VERSION="${KUBE_PROM_STACK_CHART_VERSION:-65.8.1}"
CI_MANIFEST_DIR="${CI_MANIFEST_DIR:-hack/ci/manifests}"
TREND_OBSERVE_SECONDS="${TREND_OBSERVE_SECONDS:-480}"
PLAN_SAMPLE_RETENTION="${PLAN_SAMPLE_RETENTION:-24}"
PLAN_SAMPLE_INTERVAL="${PLAN_SAMPLE_INTERVAL:-5s}"
PLAN_RECONCILE_INTERVAL="${PLAN_RECONCILE_INTERVAL:-15s}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-5}"
PROM_ENDPOINT_READY_TIMEOUT_SECONDS="${PROM_ENDPOINT_READY_TIMEOUT_SECONDS:-300}"
ALERT_ENDPOINT_READY_TIMEOUT_SECONDS="${ALERT_ENDPOINT_READY_TIMEOUT_SECONDS:-300}"
ALERT_PROPAGATION_TIMEOUT_SECONDS="${ALERT_PROPAGATION_TIMEOUT_SECONDS:-900}"
MANAGER_ENDPOINT_READY_TIMEOUT_SECONDS="${MANAGER_ENDPOINT_READY_TIMEOUT_SECONDS:-180}"

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

sed_replacement_escape() {
  printf '%s' "$1" | sed 's/[&|\\]/\\&/g'
}

log() {
  echo
  echo "==> $*"
}

cleanup() {
  if [[ -n "${MANAGER_PF_PID:-}" ]]; then
    kill "${MANAGER_PF_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${PROMETHEUS_PF_PID:-}" ]]; then
    kill "${PROMETHEUS_PF_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${ALERTMANAGER_PF_PID:-}" ]]; then
    kill "${ALERTMANAGER_PF_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

wait_until() {
  local description="$1"
  local timeout_seconds="$2"
  local interval_seconds="$3"
  shift 3

  local started_at now
  started_at="$(date +%s)"
  while true; do
    if "$@"; then
      return 0
    fi
    now="$(date +%s)"
    if (( now - started_at >= timeout_seconds )); then
      fail "timed out waiting for ${description} after ${timeout_seconds}s"
    fi
    sleep "${interval_seconds}"
  done
}

http_ok() {
  local url="$1"
  curl -fsS --max-time 5 "${url}" >/dev/null
}

port_forward_running() {
  local pid="$1"
  kill -0 "${pid}" >/dev/null 2>&1
}

prometheus_api_ready() {
  local out
  out="$(curl -fsS --max-time 5 --get --data-urlencode 'query=up' http://127.0.0.1:19090/api/v1/query || true)"
  [[ -n "${out}" ]] || return 1
  grep -q '"status":"success"' <<<"${out}"
}

prometheus_has_capacity_alerts() {
  local out
  out="$(curl -fsS --max-time 5 --get \
    --data-urlencode 'query=ALERTS{alertname=~"PVCUsageHigh|PVCUsageCritical|NamespaceBudgetBreachSoon|WorkloadBudgetBreachSoon",alertstate=~"pending|firing"}' \
    http://127.0.0.1:19090/api/v1/query || true)"
  [[ -n "${out}" ]] || return 1
  grep -q '"status":"success"' <<<"${out}" || return 1
  ! grep -Eq '"result":[[:space:]]*\[[[:space:]]*\]' <<<"${out}"
}

alertmanager_api_ready() {
  local out
  out="$(curl -fsS --max-time 5 http://127.0.0.1:19093/api/v2/status || true)"
  [[ -n "${out}" ]] || return 1
  grep -q '"cluster"' <<<"${out}"
}

alertmanager_has_capacity_alerts() {
  local out
  out="$(curl -fsS --max-time 5 http://127.0.0.1:19093/api/v2/alerts || true)"
  [[ -n "${out}" ]] || return 1
  grep -Eq '"alertname":"(PVCUsageHigh|PVCUsageCritical|NamespaceBudgetBreachSoon|WorkloadBudgetBreachSoon)"' <<<"${out}"
}

prometheus_has_workload_budget_alert() {
  local workload="$1"
  local out
  out="$(curl -fsS --max-time 5 --get \
    --data-urlencode "query=ALERTS{alertname=\"WorkloadBudgetBreachSoon\",workload=\"${workload}\",alertstate=~\"pending|firing\"}" \
    http://127.0.0.1:19090/api/v1/query || true)"
  [[ -n "${out}" ]] || return 1
  grep -q '"status":"success"' <<<"${out}" || return 1
  ! grep -Eq '"result":[[:space:]]*\[[[:space:]]*\]' <<<"${out}"
}

manager_metrics_have_capacity_series() {
  local out
  out="$(curl -fsS --max-time 5 http://127.0.0.1:18080/metrics || true)"
  [[ -n "${out}" ]] || return 1
  grep -q "^capacityplan_namespace_budget_days_to_breach" <<<"${out}" || return 1
  grep -q "^capacityplan_workload_budget_days_to_breach" <<<"${out}" || return 1
  grep -q "^capacityplan_pvc_anomaly" <<<"${out}" || return 1
}

log "Validating kubectl context"
current_context="$(kubectl config current-context 2>/dev/null || true)"
[[ -n "${current_context}" ]] || fail "kubectl current-context is empty"
[[ "${current_context}" == "${EXPECTED_KUBE_CONTEXT}" ]] || fail "kubectl context mismatch: expected ${EXPECTED_KUBE_CONTEXT}, got ${current_context}"

log "Installing kube-prometheus-stack"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null
helm repo update >/dev/null
helm_args=(
  upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack
  --version "${KUBE_PROM_STACK_CHART_VERSION}"
  --namespace "${MON_NS}"
  --create-namespace
  --wait
  --timeout 12m
  -f "${KUBE_PROM_VALUES_FILE}"
)
if [[ -n "${KUBE_PROM_VALUES_EXTRA_FILE}" ]]; then
  helm_args+=(-f "${KUBE_PROM_VALUES_EXTRA_FILE}")
fi
helm "${helm_args[@]}"

log "Waiting for monitoring CRDs and workloads"
kubectl wait --for=condition=Established crd/servicemonitors.monitoring.coreos.com --timeout=5m
kubectl wait --for=condition=Established crd/prometheusrules.monitoring.coreos.com --timeout=5m
kubectl -n "${MON_NS}" rollout status deployment/kube-prometheus-stack-operator --timeout=10m
kubectl -n "${MON_NS}" rollout status statefulset/prometheus-kube-prometheus-stack-prometheus --timeout=10m
kubectl -n "${MON_NS}" rollout status statefulset/alertmanager-kube-prometheus-stack-alertmanager --timeout=10m

log "Validating Prometheus endpoint readiness"
kubectl -n "${MON_NS}" port-forward svc/kube-prometheus-stack-prometheus 19090:9090 >/tmp/prometheus-port-forward.log 2>&1 &
PROMETHEUS_PF_PID=$!
wait_until "Prometheus port-forward process" "${PROM_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  port_forward_running "${PROMETHEUS_PF_PID}"
wait_until "Prometheus readiness endpoint" "${PROM_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  http_ok "http://127.0.0.1:19090/-/ready"
wait_until "Prometheus API query endpoint" "${PROM_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  prometheus_api_ready

log "Deploying operator manifests"
kubectl apply -k config/default

manager_deploy="$(kubectl -n "${OP_NS}" get deploy -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}')"
[[ -n "${manager_deploy}" ]] || fail "could not find controller-manager deployment in ${OP_NS}"

kubectl -n "${OP_NS}" set image "deployment/${manager_deploy}" manager="${OPERATOR_IMAGE}"

ensure_deploy_arg() {
  local deploy_name="$1"
  local arg="$2"
  local args_text
  args_text="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{range .spec.template.spec.containers[0].args[*]}{.}{"\n"}{end}')"
  if ! grep -Fxq -- "${arg}" <<<"${args_text}"; then
    local escaped_arg
    escaped_arg="$(json_escape "${arg}")"
    kubectl -n "${OP_NS}" patch "deployment/${deploy_name}" --type='json' \
      -p="[{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",\"value\":\"${escaped_arg}\"}]"
  fi
}

ensure_deploy_arg "${manager_deploy}" "--metrics-bind-address=:8080"
ensure_deploy_arg "${manager_deploy}" "--metrics-secure=false"
ensure_deploy_arg "${manager_deploy}" "--prometheus-url=${PROM_URL}"
ensure_deploy_arg "${manager_deploy}" "--debug=true"

kubectl -n "${OP_NS}" rollout status "deployment/${manager_deploy}" --timeout=10m

log "Creating PVC workload and CapacityPlan"
kubectl apply -k "${CI_MANIFEST_DIR}/workloads"

for pod in cpo-ci-steady cpo-ci-bursty cpo-ci-trickle cpo-ci-churn cpo-ci-delayed; do
  kubectl -n default wait --for=condition=PodScheduled "pod/${pod}" --timeout=3m
done
for pvc in cpo-ci-steady-pvc cpo-ci-bursty-pvc cpo-ci-trickle-pvc cpo-ci-churn-pvc cpo-ci-delayed-pvc; do
  kubectl -n default wait --for=jsonpath='{.status.phase}'=Bound "pvc/${pvc}" --timeout=5m
done

plan_manifest="/tmp/capacityplan-${PLAN_NAME}.yaml"
plan_name_escaped="$(sed_replacement_escape "${PLAN_NAME}")"
prom_url_escaped="$(sed_replacement_escape "${PROM_URL}")"
sample_retention_escaped="$(sed_replacement_escape "${PLAN_SAMPLE_RETENTION}")"
sample_interval_escaped="$(sed_replacement_escape "${PLAN_SAMPLE_INTERVAL}")"
reconcile_interval_escaped="$(sed_replacement_escape "${PLAN_RECONCILE_INTERVAL}")"
sed -e "s|__PLAN_NAME__|${plan_name_escaped}|g" \
    -e "s|__PROM_URL__|${prom_url_escaped}|g" \
    -e "s|__SAMPLE_RETENTION__|${sample_retention_escaped}|g" \
    -e "s|__SAMPLE_INTERVAL__|${sample_interval_escaped}|g" \
    -e "s|__RECONCILE_INTERVAL__|${reconcile_interval_escaped}|g" \
    "${CI_MANIFEST_DIR}/capacityplan.yaml.tmpl" > "${plan_manifest}"
kubectl apply -f "${plan_manifest}"

log "Waiting for CapacityPlan reconciliation"
last_reconcile=""
for _ in $(seq 1 60); do
  last_reconcile="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.lastReconcileTime}' 2>/dev/null || true)"
  if [[ -n "${last_reconcile}" ]]; then
    break
  fi
  sleep 5
done
[[ -n "${last_reconcile}" ]] || fail "CapacityPlan status.lastReconcileTime was not populated in time"

log "Observing storage trends for ${TREND_OBSERVE_SECONDS}s"
sleep "${TREND_OBSERVE_SECONDS}"
last_reconcile_post="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.lastReconcileTime}' 2>/dev/null || true)"
[[ -n "${last_reconcile_post}" ]] || fail "CapacityPlan did not continue reconciling during trend observation"
[[ "${last_reconcile_post}" != "${last_reconcile}" ]] || fail "CapacityPlan lastReconcileTime did not advance during trend observation"

ready_status="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
[[ "${ready_status}" == "True" ]] || fail "Ready condition is not True (got: ${ready_status})"

prom_ready_status="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.conditions[?(@.type=="PrometheusReady")].status}')"
[[ "${prom_ready_status}" == "True" ]] || fail "PrometheusReady condition is not True (got: ${prom_ready_status})"

total_pvcs="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.summary.totalPVCs}')"
[[ -n "${total_pvcs}" ]] || fail "status.summary.totalPVCs is empty"
(( total_pvcs >= 5 )) || fail "expected at least five PVCs in summary, got ${total_pvcs}"

top_risks_count="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.topRisks[*].name}' | wc -w | tr -d ' ')"
(( top_risks_count >= 1 )) || fail "expected at least one top risk after trend observation"

risk_digest="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.riskDigest}')"
[[ -n "${risk_digest}" ]] || fail "status.riskDigest is empty"

anomaly_summary="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.anomalySummary}')"
[[ -n "${anomaly_summary}" ]] || fail "status.anomalySummary is empty"

ns_forecast_scope="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.namespaceForecasts[0].scope}')"
[[ "${ns_forecast_scope}" == "namespace" ]] || fail "expected first namespace forecast scope=namespace, got ${ns_forecast_scope}"

wl_forecast_scope="$(kubectl get capacityplan "${PLAN_NAME}" -o jsonpath='{.status.workloadForecasts[0].scope}')"
[[ "${wl_forecast_scope}" == "workload" ]] || fail "expected first workload forecast scope=workload, got ${wl_forecast_scope}"

log "Validating generated PrometheusRule content"
kubectl -n default get prometheusrule "capacityplan-${PLAN_NAME}" -o yaml > /tmp/capacityplan-prometheusrule.yaml
grep -q "alert: PVCGrowthAccelerationSpike" /tmp/capacityplan-prometheusrule.yaml || fail "missing PVCGrowthAccelerationSpike alert"
grep -q "alert: PVCTrendInstability" /tmp/capacityplan-prometheusrule.yaml || fail "missing PVCTrendInstability alert"
grep -q "alert: NamespaceBudgetBreachSoon" /tmp/capacityplan-prometheusrule.yaml || fail "missing NamespaceBudgetBreachSoon alert"
grep -q "alert: WorkloadBudgetBreachSoon" /tmp/capacityplan-prometheusrule.yaml || fail "missing WorkloadBudgetBreachSoon alert"

log "Checking operator metrics for new budget/anomaly metrics"
manager_pod="$(kubectl -n "${OP_NS}" get pod -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}')"
[[ -n "${manager_pod}" ]] || fail "could not find controller-manager pod in ${OP_NS}"
kubectl -n "${OP_NS}" port-forward "pod/${manager_pod}" 18080:8080 >/tmp/manager-port-forward.log 2>&1 &
MANAGER_PF_PID=$!
wait_until "operator metrics port-forward process" "${MANAGER_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  port_forward_running "${MANAGER_PF_PID}"
wait_until "operator metrics endpoint capacity series" "${MANAGER_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  manager_metrics_have_capacity_series

log "Checking Alertmanager readiness endpoint"
kubectl -n "${MON_NS}" port-forward svc/kube-prometheus-stack-alertmanager 19093:9093 >/tmp/alertmanager-port-forward.log 2>&1 &
ALERTMANAGER_PF_PID=$!
wait_until "Alertmanager port-forward process" "${ALERT_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  port_forward_running "${ALERTMANAGER_PF_PID}"
wait_until "Alertmanager readiness endpoint" "${ALERT_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  http_ok "http://127.0.0.1:19093/-/ready"
wait_until "Alertmanager status API endpoint" "${ALERT_ENDPOINT_READY_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  alertmanager_api_ready

log "Verifying capacity alerts in Prometheus rule evaluation first"
wait_until "capacity alerts in Prometheus ALERTS metric" "${ALERT_PROPAGATION_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  prometheus_has_capacity_alerts

log "Verifying workload budget alerts for each CI workload"
for workload in cpo-ci-steady cpo-ci-bursty cpo-ci-trickle cpo-ci-churn cpo-ci-delayed; do
  wait_until "WorkloadBudgetBreachSoon for ${workload}" "${ALERT_PROPAGATION_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
    prometheus_has_workload_budget_alert "${workload}"
done

log "Verifying capacity alerts reached Alertmanager API"
wait_until "capacity alerts in Alertmanager API" "${ALERT_PROPAGATION_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  alertmanager_has_capacity_alerts

log "K3s integration checks passed"
