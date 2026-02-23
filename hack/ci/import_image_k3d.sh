#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-}"
OPERATOR_IMAGE="${OPERATOR_IMAGE:-}"
EXTRA_IMAGES="${EXTRA_IMAGES:-}"
IMPORT_RETRY_COUNT="${IMPORT_RETRY_COUNT:-1}"

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

if [[ -z "${CLUSTER_NAME}" ]]; then
  fail "CLUSTER_NAME must be set"
fi
if [[ -z "${OPERATOR_IMAGE}" ]]; then
  fail "OPERATOR_IMAGE must be set"
fi

parse_extra_images() {
  local raw="$1"
  local out=()
  local token

  # Support comma and whitespace separators.
  raw="${raw//,/ }"
  for token in ${raw}; do
    [[ -n "${token}" ]] || continue
    out+=("${token}")
  done
  printf '%s\n' "${out[@]}"
}

discover_nodes() {
  k3d node list --no-headers \
    | awk -v cluster="${CLUSTER_NAME}" '$1 ~ ("^k3d-" cluster "-") && $1 !~ /-tools$/ {print $1}'
}

candidate_image_refs() {
  local img="$1"
  echo "${img}"

  # If image does not include an explicit registry host, also match docker.io/library form.
  local first
  first="${img%%/*}"
  if [[ "${img}" != */* ]] || ([[ "${first}" != *.* ]] && [[ "${first}" != *:* ]] && [[ "${first}" != "localhost" ]]); then
    echo "docker.io/library/${img}"
  fi
}

node_has_image() {
  local node="$1"
  local refs="$2"
  local listed
  listed="$(k3d node exec "${node}" -- ctr -n k8s.io images ls -q || true)"
  [[ -n "${listed}" ]] || return 1
  while IFS= read -r ref; do
    [[ -n "${ref}" ]] || continue
    if grep -Fxq "${ref}" <<<"${listed}"; then
      return 0
    fi
  done <<<"${refs}"
  return 1
}

check_image_on_all_nodes() {
  local missing=0
  local nodes=()
  local image refs

  while IFS= read -r node; do
    [[ -n "${node}" ]] || continue
    nodes+=("${node}")
  done < <(discover_nodes)

  if [[ ${#nodes[@]} -eq 0 ]]; then
    fail "no k3d nodes found for cluster ${CLUSTER_NAME}"
  fi

  echo "Verifying image on nodes: ${nodes[*]}"
  for image in "${IMAGES_TO_IMPORT[@]}"; do
    refs="$(candidate_image_refs "${image}")"
    for node in "${nodes[@]}"; do
      if ! node_has_image "${node}" "${refs}"; then
        echo "missing image on ${node}: ${image}" >&2
        missing=1
      fi
    done
  done
  return "${missing}"
}

IMAGES_TO_IMPORT=("${OPERATOR_IMAGE}")
while IFS= read -r extra; do
  [[ -n "${extra}" ]] || continue
  IMAGES_TO_IMPORT+=("${extra}")
done < <(parse_extra_images "${EXTRA_IMAGES}")

echo "Importing images into cluster ${CLUSTER_NAME}: ${IMAGES_TO_IMPORT[*]}"
k3d image import "${IMAGES_TO_IMPORT[@]}" -c "${CLUSTER_NAME}" || true
if check_image_on_all_nodes; then
  echo "Image import verification passed"
  exit 0
fi

for attempt in $(seq 1 "${IMPORT_RETRY_COUNT}"); do
  echo "Retrying image import (${attempt}/${IMPORT_RETRY_COUNT})..." >&2
  sleep 2
  k3d image import "${IMAGES_TO_IMPORT[@]}" -c "${CLUSTER_NAME}" || true
  if check_image_on_all_nodes; then
    echo "Image import verification passed after retry"
    exit 0
  fi
done

fail "one or more images are not present on all nodes in cluster ${CLUSTER_NAME} after retries: ${IMAGES_TO_IMPORT[*]}"
