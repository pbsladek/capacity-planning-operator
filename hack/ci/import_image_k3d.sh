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
    | awk -v cluster="${CLUSTER_NAME}" '$1 ~ ("^k3d-" cluster "-(server|agent)-[0-9]+$") {print $1}'
}

candidate_image_refs() {
  local img="$1"
  echo "${img}"

  # If image does not include an explicit registry host, also match common docker.io forms.
  local first
  first="${img%%/*}"
  if [[ "${img}" != */* ]]; then
    echo "docker.io/library/${img}"
    echo "index.docker.io/library/${img}"
    return
  fi

  if [[ "${first}" != *.* ]] && [[ "${first}" != *:* ]] && [[ "${first}" != "localhost" ]]; then
    echo "docker.io/${img}"
    echo "index.docker.io/${img}"
  fi
}

candidate_image_patterns() {
  local img="$1"
  local ref="${img%@*}"
  local base tag last
  last="${ref##*/}"
  if [[ "${last}" == *:* ]]; then
    base="${ref%:*}"
    tag="${ref##*:}"
  else
    base="${ref}"
    tag=""
  fi
  local name="${base##*/}"

  # Exact image if no tag parsing happened.
  if [[ -z "${tag}" ]]; then
    echo "${img}"
    return
  fi

  echo "${base}:${tag}"
  echo "docker.io/${base}:${tag}"
  echo "index.docker.io/${base}:${tag}"
  if [[ "${base}" != */* ]]; then
    echo "docker.io/library/${name}:${tag}"
    echo "index.docker.io/library/${name}:${tag}"
  fi
  echo "(^|.*/)${name}:${tag}(@sha256:[a-f0-9]+)?$"
}

node_has_image() {
  local node="$1"
  local refs="$2"
  local patterns="$3"
  local listed
  listed="$(docker exec "${node}" sh -lc 'k3s ctr -n k8s.io images ls 2>/dev/null || k3s ctr images ls 2>/dev/null || ctr -n k8s.io images ls 2>/dev/null || ctr images ls 2>/dev/null || true' \
    | awk 'NR>1 {print $1}' || true)"
  [[ -n "${listed}" ]] || return 1
  while IFS= read -r ref; do
    [[ -n "${ref}" ]] || continue
    if grep -Fxq "${ref}" <<<"${listed}"; then
      return 0
    fi
  done <<<"${refs}"
  while IFS= read -r pat; do
    [[ -n "${pat}" ]] || continue
    if grep -Eq "${pat}" <<<"${listed}"; then
      return 0
    fi
  done <<<"${patterns}"
  return 1
}

ensure_local_images_present() {
  local image
  for image in "${IMAGES_TO_IMPORT[@]}"; do
    if docker image inspect "${image}" >/dev/null 2>&1; then
      continue
    fi
    echo "Local image not found, pulling: ${image}"
    docker pull "${image}"
  done
}

check_image_on_all_nodes() {
  local missing=0
  local nodes=()
  local image refs patterns

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
    patterns="$(candidate_image_patterns "${image}")"
    for node in "${nodes[@]}"; do
      if ! node_has_image "${node}" "${refs}" "${patterns}"; then
        echo "missing image on ${node}: ${image}" >&2
        docker exec "${node}" sh -lc 'k3s ctr -n k8s.io images ls 2>/dev/null || k3s ctr images ls 2>/dev/null || ctr -n k8s.io images ls 2>/dev/null || ctr images ls 2>/dev/null || true' \
          | sed 's/^/  [node images] /' >&2 || true
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

ensure_local_images_present

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
