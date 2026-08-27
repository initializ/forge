#!/usr/bin/env bash
# Install the OpenTelemetry Collector (daemonset) → Datadog for Forge traces.
# Idempotent: safe to re-run (helm upgrade --install, kubectl apply-style guards).
#
# Usage:
#   DD_API_KEY=<your-datadog-api-key> ./install.sh
set -euo pipefail

NAMESPACE="${NAMESPACE:-monitoring}"
RELEASE="${RELEASE:-otel-collector}"
VALUES="${VALUES:-$(dirname "$0")/values.yaml}"

if [[ -z "${DD_API_KEY:-}" ]]; then
  echo "ERROR: set DD_API_KEY in the environment (Datadog US1 API key)." >&2
  exit 1
fi

helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update open-telemetry

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# Create/refresh the API-key Secret without echoing it into shell history/logs.
kubectl create secret generic datadog-secret \
  --namespace "$NAMESPACE" \
  --from-literal=DD_API_KEY="$DD_API_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -

helm upgrade --install "$RELEASE" open-telemetry/opentelemetry-collector \
  --namespace "$NAMESPACE" \
  --values "$VALUES"

kubectl -n "$NAMESPACE" rollout status daemonset/"$RELEASE" --timeout=120s
echo "Collector ready. Point Forge at http://otel-collector.${NAMESPACE}.svc.cluster.local:4318/v1/traces"
