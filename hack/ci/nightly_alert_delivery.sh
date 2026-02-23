#!/usr/bin/env bash
set -euo pipefail

MON_NS="${MON_NS:-monitoring}"
RULE_NAME="${RULE_NAME:-ci-always-firing}"

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

log() {
  echo
  echo "==> $*"
}

log "Deploying Alertmanager webhook receiver"
cat <<'EOF' | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: alert-receiver
  namespace: monitoring
spec:
  replicas: 1
  selector:
    matchLabels:
      app: alert-receiver
  template:
    metadata:
      labels:
        app: alert-receiver
    spec:
      containers:
        - name: receiver
          image: python:3.12-alpine
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "-c"]
          args:
            - |
              python -u -c '
              from http.server import BaseHTTPRequestHandler, HTTPServer
              class H(BaseHTTPRequestHandler):
                  def do_POST(self):
                      n = int(self.headers.get("Content-Length", "0"))
                      body = self.rfile.read(n).decode("utf-8", "replace")
                      print("ALERT_RECEIVED path=%s bytes=%d" % (self.path, len(body)), flush=True)
                      self.send_response(200)
                      self.end_headers()
                      self.wfile.write(b"ok")
                  def log_message(self, fmt, *args):
                      return
              HTTPServer(("0.0.0.0", 8080), H).serve_forever()
              '
          ports:
            - containerPort: 8080
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 128Mi
---
apiVersion: v1
kind: Service
metadata:
  name: alert-receiver
  namespace: monitoring
spec:
  selector:
    app: alert-receiver
  ports:
    - name: http
      port: 8080
      targetPort: 8080
EOF

kubectl -n "${MON_NS}" rollout status deployment/alert-receiver --timeout=5m

log "Applying always-firing PrometheusRule"
cat <<EOF | kubectl apply -f -
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: ${RULE_NAME}
  namespace: ${MON_NS}
  labels:
    release: kube-prometheus-stack
spec:
  groups:
    - name: ci.alert.delivery
      rules:
        - alert: CIAlwaysFiring
          expr: vector(1)
          for: 1m
          labels:
            severity: warning
          annotations:
            summary: "CI alert delivery test"
            description: "Synthetic alert for nightly Alertmanager delivery-path validation."
EOF

log "Waiting for webhook delivery"
for _ in $(seq 1 60); do
  if kubectl -n "${MON_NS}" logs deploy/alert-receiver --tail=200 2>/dev/null | grep -q "ALERT_RECEIVED"; then
    log "Alert delivery confirmed"
    exit 0
  fi
  sleep 10
done

kubectl -n "${MON_NS}" logs deploy/alert-receiver --tail=400 || true
fail "did not observe ALERT_RECEIVED in receiver logs within timeout"
