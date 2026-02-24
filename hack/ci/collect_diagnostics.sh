#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${OUT_DIR:-/tmp/cpo-ci-diagnostics}"
mkdir -p "${OUT_DIR}/fallback"

run_capture() {
  local name="$1"
  shift
  local out="${OUT_DIR}/fallback/${name}.txt"
  {
    echo "### command: $*"
    echo "### timestamp_utc: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo
    "$@"
  } >"${out}" 2>&1 || true
}

if ! bash hack/ci/run_ci_runner.sh collect-diagnostics "$@"; then
  echo "warning: ci-runner collect-diagnostics failed; continuing with fallback capture" | tee "${OUT_DIR}/fallback/collect-diagnostics-warning.txt"
fi

run_capture kubectl_version kubectl version
run_capture kubectl_context kubectl config current-context
run_capture helm_list helm list -A
run_capture helm_get_values_kps helm get values kube-prometheus-stack -n monitoring -a

run_capture nodes kubectl get nodes -o wide
run_capture pods_all kubectl get pods -A -o wide
run_capture events_all kubectl get events -A --sort-by=.lastTimestamp

run_capture operator_resources_yaml kubectl get deploy,rs,po,svc,sa,role,rolebinding -n k8s-operator-system -o yaml
run_capture monitoring_resources_yaml kubectl get deploy,sts,ds,po,svc,cm,secrets -n monitoring -o yaml
run_capture capacityplan_yaml kubectl get capacityplan ci-plan -o yaml
run_capture capacityplan_describe kubectl describe capacityplan ci-plan

run_capture operator_deploy_describe kubectl describe deploy k8s-operator-controller-manager -n k8s-operator-system
run_capture operator_pods_describe kubectl describe pods -n k8s-operator-system
run_capture manager_logs kubectl logs deploy/k8s-operator-controller-manager -n k8s-operator-system --all-containers=true --tail=-1

run_capture llm_resources_yaml kubectl get deploy,po,svc -n llm -o yaml
run_capture llm_pods_describe kubectl describe pods -n llm
run_capture llm_logs kubectl logs deploy/ollama -n llm --all-containers=true --tail=-1

run_capture kube_prom_operator_logs kubectl logs deploy/kube-prometheus-stack-operator -n monitoring --all-containers=true --tail=-1
run_capture prometheus_logs kubectl logs statefulset/prometheus-kube-prometheus-stack-prometheus -n monitoring -c prometheus --tail=-1
run_capture alertmanager_logs kubectl logs statefulset/alertmanager-kube-prometheus-stack-alertmanager -n monitoring -c alertmanager --tail=-1

run_capture crds_monitoring kubectl get crd servicemonitors.monitoring.coreos.com prometheusrules.monitoring.coreos.com -o yaml
run_capture servicemonitors_all kubectl get servicemonitors.monitoring.coreos.com -A -o yaml
run_capture prometheusrules_all kubectl get prometheusrules.monitoring.coreos.com -A -o yaml
