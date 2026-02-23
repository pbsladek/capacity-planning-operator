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
USAGE_SNAPSHOT_INTERVAL_SECONDS="${USAGE_SNAPSHOT_INTERVAL_SECONDS:-180}"
USAGE_RATIO_SANITY_MAX="${USAGE_RATIO_SANITY_MAX:-0}"
MIN_GROWTH_BYTES_PER_MIN="${MIN_GROWTH_BYTES_PER_MIN:-1024}"
MIN_GROWING_PVCS="${MIN_GROWING_PVCS:-3}"
PLAN_SAMPLE_RETENTION="${PLAN_SAMPLE_RETENTION:-24}"
PLAN_SAMPLE_INTERVAL="${PLAN_SAMPLE_INTERVAL:-5s}"
PLAN_RECONCILE_INTERVAL="${PLAN_RECONCILE_INTERVAL:-15s}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-5}"
PROM_ENDPOINT_READY_TIMEOUT_SECONDS="${PROM_ENDPOINT_READY_TIMEOUT_SECONDS:-300}"
ALERT_ENDPOINT_READY_TIMEOUT_SECONDS="${ALERT_ENDPOINT_READY_TIMEOUT_SECONDS:-300}"
ALERT_PROPAGATION_TIMEOUT_SECONDS="${ALERT_PROPAGATION_TIMEOUT_SECONDS:-900}"
MANAGER_ENDPOINT_READY_TIMEOUT_SECONDS="${MANAGER_ENDPOINT_READY_TIMEOUT_SECONDS:-180}"
MANAGER_ROLLOUT_TIMEOUT_SECONDS="${MANAGER_ROLLOUT_TIMEOUT_SECONDS:-420}"
ROLLOUT_STATUS_INTERVAL_SECONDS="${ROLLOUT_STATUS_INTERVAL_SECONDS:-15}"

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
  curl -fs --max-time 5 "${url}" >/dev/null 2>&1
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

prometheus_instant_scalar() {
  local query="$1"
  local out val
  out="$(curl -fsS --max-time 5 --get --data-urlencode "query=${query}" http://127.0.0.1:19090/api/v1/query || true)"
  [[ -n "${out}" ]] || return 1
  val="$(printf '%s' "${out}" | sed -n 's/.*"value":[[][^,]*,"\([^"]*\)"[]].*/\1/p' | head -n1)"
  [[ -n "${val}" ]] || return 1
  printf '%s' "${val}"
}

print_capacityplan_usage_snapshot() {
  local now rows
  now="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  echo "[$now] CapacityPlan PVC snapshot (${PLAN_NAME})"
  rows="$(kubectl get capacityplan "${PLAN_NAME}" \
    -o jsonpath='{range .status.pvcs[*]}{.name}{"\t"}{.usedBytes}{"\t"}{.samplesCount}{"\t"}{.usageRatio}{"\t"}{.growthBytesPerDay}{"\n"}{end}' 2>/dev/null || true)"
  if [[ -z "${rows}" ]]; then
    echo "  status.pvcs is empty"
    return
  fi
  echo "  pvc usedBytes samples usageRatio growthBytesPerDay growthBytesPerMin"
  while IFS=$'\t' read -r name used samples ratio growth; do
    [[ -n "${name}" ]] || continue
    local growth_per_min
    growth_per_min="$(awk -v g="${growth:-0}" 'BEGIN { printf "%.2f", g/1440 }')"
    echo "  ${name} ${used:-0} ${samples:-0} ${ratio:-0} ${growth:-0} ${growth_per_min}"
  done <<<"${rows}"
}

capacityplan_has_nonzero_usage() {
  local rows used
  rows="$(kubectl get capacityplan "${PLAN_NAME}" \
    -o jsonpath='{range .status.pvcs[*]}{.usedBytes}{"\n"}{end}' 2>/dev/null || true)"
  [[ -n "${rows}" ]] || return 1
  while IFS= read -r used; do
    [[ -n "${used}" ]] || continue
    if [[ "${used}" =~ ^[0-9]+$ ]] && (( used > 0 )); then
      return 0
    fi
  done <<<"${rows}"
  return 1
}

capacityplan_count_growing_pvcs() {
  local rows count
  rows="$(kubectl get capacityplan "${PLAN_NAME}" \
    -o jsonpath='{range .status.pvcs[*]}{.name}{"\t"}{.growthBytesPerDay}{"\n"}{end}' 2>/dev/null || true)"
  [[ -n "${rows}" ]] || {
    echo 0
    return
  }
  count="$(printf '%s\n' "${rows}" \
    | awk -F'\t' -v min_per_min="${MIN_GROWTH_BYTES_PER_MIN}" '
      BEGIN { c=0; min_day=min_per_min*1440 }
      NF>=2 {
        g=$2+0
        if (g > min_day) c++
      }
      END { print c+0 }')"
  echo "${count}"
}

print_growth_per_min_summary() {
  local rows now
  rows="$(kubectl get capacityplan "${PLAN_NAME}" \
    -o jsonpath='{range .status.pvcs[*]}{.name}{"\t"}{.growthBytesPerDay}{"\n"}{end}' 2>/dev/null || true)"
  now="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  echo "[$now] Derived growth summary (bytes/min)"
  if [[ -z "${rows}" ]]; then
    echo "  status.pvcs is empty"
    return
  fi
  echo "  pvc growthBytesPerMin"
  while IFS=$'\t' read -r name growth_day; do
    [[ -n "${name}" ]] || continue
    growth_per_min="$(awk -v g="${growth_day:-0}" 'BEGIN { printf "%.2f", g/1440 }')"
    echo "  ${name} ${growth_per_min}"
  done <<<"${rows}"
}

capacityplan_has_invalid_usage_ratio() {
  local rows bad
  if ! awk -v max="${USAGE_RATIO_SANITY_MAX}" 'BEGIN { exit !(max > 0) }'; then
    return 1
  fi
  rows="$(kubectl get capacityplan "${PLAN_NAME}" \
    -o jsonpath='{range .status.pvcs[*]}{.name}{"\t"}{.usageRatio}{"\t"}{.usedBytes}{"\t"}{.capacityBytes}{"\n"}{end}' 2>/dev/null || true)"
  [[ -n "${rows}" ]] || return 1
  bad="$(printf '%s\n' "${rows}" \
    | awk -F'\t' -v max="${USAGE_RATIO_SANITY_MAX}" 'NF>=4 && ($2+0) > max {printf "%s ratio=%s used=%s cap=%s\n", $1, $2, $3, $4}')"
  if [[ -n "${bad}" ]]; then
    echo "Detected invalid usage ratios (> ${USAGE_RATIO_SANITY_MAX}):" >&2
    printf '%s\n' "${bad}" | sed 's/^/  /' >&2
    return 0
  fi
  return 1
}

print_prometheus_pvc_raw_snapshot() {
  local now pvc used cap series ratio
  now="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  echo "[$now] Prometheus PVC raw snapshot (default namespace)"
  echo "  pvc usedBytes capBytes ratio usedSeriesCount"
  for pvc in cpo-ci-steady-pvc cpo-ci-bursty-pvc cpo-ci-trickle-pvc cpo-ci-churn-pvc cpo-ci-delayed-pvc; do
    used="$(prometheus_instant_scalar "max(kubelet_volume_stats_used_bytes{namespace=\"default\",persistentvolumeclaim=\"${pvc}\"})" || true)"
    cap="$(prometheus_instant_scalar "max(kubelet_volume_stats_capacity_bytes{namespace=\"default\",persistentvolumeclaim=\"${pvc}\"})" || true)"
    series="$(prometheus_instant_scalar "count(kubelet_volume_stats_used_bytes{namespace=\"default\",persistentvolumeclaim=\"${pvc}\"})" || true)"
    ratio="n/a"
    if [[ "${used}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]] && [[ "${cap}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]]; then
      ratio="$(awk -v u="${used}" -v c="${cap}" 'BEGIN { if (c > 0) printf "%.6f", u/c; else printf "n/a" }')"
    fi
    echo "  ${pvc} ${used:-n/a} ${cap:-n/a} ${ratio} ${series:-n/a}"
  done
}

prometheus_has_nonzero_pvc_usage() {
  local pvc used
  for pvc in cpo-ci-steady-pvc cpo-ci-bursty-pvc cpo-ci-trickle-pvc cpo-ci-churn-pvc cpo-ci-delayed-pvc; do
    used="$(prometheus_instant_scalar "max(kubelet_volume_stats_used_bytes{namespace=\"default\",persistentvolumeclaim=\"${pvc}\"})" || true)"
    if [[ "${used}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]]; then
      if awk -v u="${used}" 'BEGIN { exit !(u > 0) }'; then
        return 0
      fi
    fi
  done
  return 1
}

to_int_or_zero() {
  local v="${1:-}"
  if [[ "${v}" =~ ^[0-9]+$ ]]; then
    printf '%s' "${v}"
  else
    printf '0'
  fi
}

dump_manager_rollout_diagnostics() {
  local deploy_name="$1"
  echo
  echo "---- rollout diagnostics (${OP_NS}/${deploy_name}) ----"
  kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o wide || true
  kubectl -n "${OP_NS}" describe "deployment/${deploy_name}" || true
  kubectl -n "${OP_NS}" get rs -l control-plane=controller-manager -o wide --sort-by=.metadata.creationTimestamp || true
  kubectl -n "${OP_NS}" get pods -l control-plane=controller-manager -o wide || true
  kubectl -n "${OP_NS}" logs "deployment/${deploy_name}" --tail=200 || true
  echo "---- end rollout diagnostics ----"
}

new_manager_rs_has_image_pull_error() {
  local rs_lines target_hash image hash
  rs_lines="$(kubectl -n "${OP_NS}" get rs -l control-plane=controller-manager --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{range .items[*]}{.spec.template.spec.containers[0].image}{"\t"}{.metadata.labels.pod-template-hash}{"\n"}{end}' 2>/dev/null || true)"

  target_hash=""
  while IFS=$'\t' read -r image hash; do
    [[ -n "${image}" ]] || continue
    [[ -n "${hash}" ]] || continue
    if [[ "${image}" == "${OPERATOR_IMAGE}" ]]; then
      target_hash="${hash}"
    fi
  done <<<"${rs_lines}"
  [[ -n "${target_hash}" ]] || return 1

  kubectl -n "${OP_NS}" get pods -l "control-plane=controller-manager,pod-template-hash=${target_hash}" \
    -o jsonpath='{range .items[*]}{range .status.containerStatuses[*]}{.state.waiting.reason}{"\n"}{end}{end}' 2>/dev/null \
    | grep -Eq '^(ImagePullBackOff|ErrImagePull)$'
}

wait_for_manager_rollout() {
  local deploy_name="$1"
  local timeout_seconds="$2"
  local status_interval_seconds="$3"
  local start_ts now elapsed last_status
  start_ts="$(date +%s)"
  last_status=0

  while true; do
    local desired updated ready available unavailable generation observed old
    desired="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
    updated="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.status.updatedReplicas}' 2>/dev/null || true)"
    ready="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
    available="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || true)"
    unavailable="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.status.unavailableReplicas}' 2>/dev/null || true)"
    generation="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.metadata.generation}' 2>/dev/null || true)"
    observed="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)"

    desired="$(to_int_or_zero "${desired}")"
    updated="$(to_int_or_zero "${updated}")"
    ready="$(to_int_or_zero "${ready}")"
    available="$(to_int_or_zero "${available}")"
    unavailable="$(to_int_or_zero "${unavailable}")"
    generation="$(to_int_or_zero "${generation}")"
    observed="$(to_int_or_zero "${observed}")"
    old=$((desired - updated))
    if (( old < 0 )); then
      old=0
    fi

    if (( desired > 0 && observed >= generation && updated >= desired && available >= desired && ready >= desired && unavailable == 0 )); then
      log "Manager rollout complete: desired=${desired} updated=${updated} ready=${ready} available=${available}"
      return 0
    fi

    now="$(date +%s)"
    elapsed=$((now - start_ts))
    if (( now - last_status >= status_interval_seconds )); then
      echo "rollout progress (${elapsed}s): desired=${desired} updated=${updated} old=${old} ready=${ready} available=${available} unavailable=${unavailable} observedGeneration=${observed}/${generation}"
      kubectl -n "${OP_NS}" get rs -l control-plane=controller-manager -o wide --sort-by=.metadata.creationTimestamp || true
      kubectl -n "${OP_NS}" get pods -l control-plane=controller-manager -o wide || true
      last_status="${now}"
    fi

    # Fail fast on terminal image pull states.
    if new_manager_rs_has_image_pull_error; then
      dump_manager_rollout_diagnostics "${deploy_name}"
      fail "deployment/${deploy_name} new ReplicaSet hit ImagePullBackOff/ErrImagePull during rollout"
    fi

    if (( elapsed >= timeout_seconds )); then
      dump_manager_rollout_diagnostics "${deploy_name}"
      fail "timed out waiting for deployment/${deploy_name} rollout after ${timeout_seconds}s"
    fi
    sleep 5
  done
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

ensure_manager_deploy() {
  local deploy_name="$1"
  shift
  local desired_args=("$@")
  local args_text current_image patch_ops escaped
  local missing_args=()

  args_text="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{range .spec.template.spec.containers[0].args[*]}{.}{"\n"}{end}')"
  current_image="$(kubectl -n "${OP_NS}" get "deployment/${deploy_name}" -o jsonpath='{.spec.template.spec.containers[0].image}')"

  patch_ops=""
  if [[ "${current_image}" != "${OPERATOR_IMAGE}" ]]; then
    escaped="$(json_escape "${OPERATOR_IMAGE}")"
    patch_ops+="{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"${escaped}\"}"
  fi
  patch_ops+="${patch_ops:+,}{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/imagePullPolicy\",\"value\":\"Never\"}"

  for arg in "${desired_args[@]}"; do
    if ! grep -Fxq -- "${arg}" <<<"${args_text}"; then
      missing_args+=("${arg}")
    fi
  done

  for arg in "${missing_args[@]}"; do
    escaped="$(json_escape "${arg}")"
    if [[ -n "${patch_ops}" ]]; then
      patch_ops+=","
    fi
    patch_ops+="{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",\"value\":\"${escaped}\"}"
  done

  if [[ -n "${patch_ops}" ]]; then
    kubectl -n "${OP_NS}" patch "deployment/${deploy_name}" --type='json' -p="[${patch_ops}]"
  fi
}

ensure_manager_deploy "${manager_deploy}" \
  "--metrics-bind-address=:8080" \
  "--metrics-secure=false" \
  "--prometheus-url=${PROM_URL}" \
  "--debug=true"

wait_for_manager_rollout "${manager_deploy}" "${MANAGER_ROLLOUT_TIMEOUT_SECONDS}" "${ROLLOUT_STATUS_INTERVAL_SECONDS}"

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
remaining="${TREND_OBSERVE_SECONDS}"
saw_nonzero_usage=0
saw_capacity_alerts=0
max_growing_pvcs=0
while (( remaining > 0 )); do
  interval="${USAGE_SNAPSHOT_INTERVAL_SECONDS}"
  if (( interval <= 0 )); then
    interval=60
  fi
  if (( interval > remaining )); then
    interval="${remaining}"
  fi
  sleep "${interval}"
  remaining=$((remaining - interval))
  print_capacityplan_usage_snapshot
  print_growth_per_min_summary
  print_prometheus_pvc_raw_snapshot
  growing_pvcs_now="$(capacityplan_count_growing_pvcs)"
  echo "  growingPVCsAboveThreshold=${growing_pvcs_now} thresholdBytesPerMin=${MIN_GROWTH_BYTES_PER_MIN}"
  if (( growing_pvcs_now > max_growing_pvcs )); then
    max_growing_pvcs="${growing_pvcs_now}"
  fi
  if capacityplan_has_nonzero_usage || prometheus_has_nonzero_pvc_usage; then
    saw_nonzero_usage=1
  fi
  if prometheus_has_capacity_alerts; then
    saw_capacity_alerts=1
  fi
  if capacityplan_has_invalid_usage_ratio; then
    kubectl get capacityplan "${PLAN_NAME}" -o yaml || true
    fail "capacity plan usage ratio exceeded sanity limit (${USAGE_RATIO_SANITY_MAX}); metrics source likely incorrect"
  fi
done

if (( saw_nonzero_usage == 0 )); then
  kubectl get capacityplan "${PLAN_NAME}" -o yaml || true
  fail "all PVC usedBytes remained 0 throughout trend observation; no growth signal detected from metrics"
fi
if (( max_growing_pvcs < MIN_GROWING_PVCS )); then
  kubectl get capacityplan "${PLAN_NAME}" -o yaml || true
  fail "peak growing PVC count was ${max_growing_pvcs}; required at least ${MIN_GROWING_PVCS} PVCs above ${MIN_GROWTH_BYTES_PER_MIN} bytes/min during observation"
fi

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
if (( saw_capacity_alerts == 0 )); then
  wait_until "capacity alerts in Prometheus ALERTS metric" "${ALERT_PROPAGATION_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
    prometheus_has_capacity_alerts
fi

log "Verifying workload budget alerts for each CI workload"
for workload in cpo-ci-steady cpo-ci-bursty cpo-ci-trickle cpo-ci-churn cpo-ci-delayed; do
  wait_until "WorkloadBudgetBreachSoon for ${workload}" "${ALERT_PROPAGATION_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
    prometheus_has_workload_budget_alert "${workload}"
done

log "Verifying capacity alerts reached Alertmanager API"
wait_until "capacity alerts in Alertmanager API" "${ALERT_PROPAGATION_TIMEOUT_SECONDS}" "${POLL_INTERVAL_SECONDS}" \
  alertmanager_has_capacity_alerts

log "K3s integration checks passed"
