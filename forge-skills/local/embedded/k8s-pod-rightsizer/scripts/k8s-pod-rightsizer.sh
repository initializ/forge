#!/usr/bin/env bash
# k8s-pod-rightsizer.sh — Analyze Kubernetes workload metrics and produce
# policy-constrained CPU/memory rightsizing recommendations.
#
# Usage: ./k8s-pod-rightsizer.sh '{"namespace":"prod","mode":"dry-run"}'
#
# Requires: kubectl, jq, curl (for Prometheus), bash.
set -euo pipefail

###############################################################################
# Constants & Defaults
###############################################################################

# Default policy values (used when no POLICY_FILE is provided)
DEFAULT_CPU_SAFETY_FACTOR="1.25"
DEFAULT_MEMORY_SAFETY_FACTOR="1.35"
DEFAULT_CPU_BURST_MULTIPLIER="2.0"
DEFAULT_MEMORY_BURST_MULTIPLIER="1.5"
DEFAULT_CPU_MIN_MILLI="10"
DEFAULT_CPU_MAX_MILLI="8000"
DEFAULT_MEMORY_MIN_MI="32"
DEFAULT_MEMORY_MAX_MI="32768"
DEFAULT_STEP_PERCENT="15"
DEFAULT_LOOKBACK="24h"

# Metrics source flag
METRICS_SOURCE="none"
ADVISORY_ONLY="false"

# Temp directory with cleanup trap
TMPDIR_WORK=$(mktemp -d)
trap 'rm -rf "$TMPDIR_WORK"' EXIT

###############################################################################
# Input Parsing & Validation
###############################################################################

INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error":"usage: k8s-pod-rightsizer.sh {\"namespace\":\"...\",\"mode\":\"dry-run\"}"}' >&2
  exit 1
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error":"invalid JSON input"}' >&2
  exit 1
fi

NAMESPACE=$(echo "$INPUT" | jq -r '.namespace // empty')
WORKLOAD=$(echo "$INPUT" | jq -r '.workload // empty')
LABEL_SELECTOR=$(echo "$INPUT" | jq -r '.label_selector // empty')
MODE=$(echo "$INPUT" | jq -r '.mode // "dry-run"')
I_ACCEPT_RISK=$(echo "$INPUT" | jq -r '.i_accept_risk // false')
POLICY_FILE_INPUT=$(echo "$INPUT" | jq -r '.policy_file // empty')
LOOKBACK=$(echo "$INPUT" | jq -r '.lookback // empty')
OUTPUT_FORMAT=$(echo "$INPUT" | jq -r '.output_format // "markdown"')

# Resolve namespace
if [ -z "$NAMESPACE" ]; then
  NAMESPACE="${DEFAULT_NAMESPACE:-}"
fi
if [ -z "$NAMESPACE" ]; then
  echo '{"error":"namespace is required (provide in input or set DEFAULT_NAMESPACE)"}' >&2
  exit 1
fi

# Normalize mode — map common synonyms to canonical values
case "$MODE" in
  dry-run|dryrun|dry_run|report|check|analyze|analysis|overprovisioned|over-provisioned|underprovisioned|under-provisioned)
    MODE="dry-run"
    ;;
  plan|patch|patches|generate-patches|generate_patches|diff)
    MODE="plan"
    ;;
  apply|execute|run)
    MODE="apply"
    ;;
  *)
    echo "{\"error\":\"invalid mode '$MODE': must be dry-run, plan, or apply\"}" >&2
    exit 1
    ;;
esac

# Validate apply prerequisites
if [ "$MODE" = "apply" ] && [ "$I_ACCEPT_RISK" != "true" ]; then
  echo '{"error":"apply mode requires i_accept_risk: true"}' >&2
  exit 1
fi

# Validate output format
case "$OUTPUT_FORMAT" in
  markdown|json|yaml) ;;
  *)
    echo "{\"error\":\"invalid output_format '$OUTPUT_FORMAT': must be markdown, json, or yaml\"}" >&2
    exit 1
    ;;
esac

# Set lookback with default
if [ -z "$LOOKBACK" ]; then
  LOOKBACK="$DEFAULT_LOOKBACK"
fi

# Validate lookback format and cap at 30d
LOOKBACK_HOURS=$(echo "$LOOKBACK" | jq -Rr '
  if test("^[0-9]+h$") then ltrimstr("") | rtrimstr("h") | tonumber
  elif test("^[0-9]+d$") then ltrimstr("") | rtrimstr("d") | tonumber * 24
  else -1
  end
')
if [ "$LOOKBACK_HOURS" -lt 0 ] 2>/dev/null; then
  echo '{"error":"invalid lookback format: use Nh or Nd (e.g., 24h, 7d)"}' >&2
  exit 1
fi
if [ "$LOOKBACK_HOURS" -gt 720 ]; then
  echo '{"error":"lookback cannot exceed 30d (720h)"}' >&2
  exit 1
fi

# Resolve policy file
POLICY_FILE="${POLICY_FILE_INPUT:-${POLICY_FILE:-}}"

###############################################################################
# Policy Functions
###############################################################################

policy_load() {
  if [ -n "$POLICY_FILE" ] && [ -f "$POLICY_FILE" ]; then
    if ! jq empty "$POLICY_FILE" 2>/dev/null; then
      echo '{"error":"invalid JSON in policy file"}' >&2
      exit 1
    fi
    cat "$POLICY_FILE"
  else
    # Return empty policy (will use defaults)
    echo '{}'
  fi
}

resolve_policy() {
  local policy_json="$1"
  local ns="$2"
  local workload_key="$3"  # "namespace/name" or empty

  # Build effective policy: defaults → namespace override → workload override
  jq -n --argjson policy "$policy_json" \
    --arg ns "$ns" \
    --arg wk "$workload_key" \
    --argjson d_csf "$DEFAULT_CPU_SAFETY_FACTOR" \
    --argjson d_msf "$DEFAULT_MEMORY_SAFETY_FACTOR" \
    --argjson d_cbm "$DEFAULT_CPU_BURST_MULTIPLIER" \
    --argjson d_mbm "$DEFAULT_MEMORY_BURST_MULTIPLIER" \
    --argjson d_cmin "$DEFAULT_CPU_MIN_MILLI" \
    --argjson d_cmax "$DEFAULT_CPU_MAX_MILLI" \
    --argjson d_mmin "$DEFAULT_MEMORY_MIN_MI" \
    --argjson d_mmax "$DEFAULT_MEMORY_MAX_MI" \
    --argjson d_step "$DEFAULT_STEP_PERCENT" '
    {
      cpu_safety_factor: $d_csf,
      memory_safety_factor: $d_msf,
      cpu_burst_multiplier: $d_cbm,
      memory_burst_multiplier: $d_mbm,
      cpu_min_milli: $d_cmin,
      cpu_max_milli: $d_cmax,
      memory_min_mi: $d_mmin,
      memory_max_mi: $d_mmax,
      step_percent: $d_step
    } as $builtin_defaults |
    ($policy.defaults // {}) as $user_defaults |
    ($policy.namespaces[$ns] // {}) as $ns_override |
    (if $wk != "" then ($policy.workloads[$wk] // {}) else {} end) as $wk_override |
    # Merge user defaults over builtin, converting cpu_min/memory_min string values
    ($builtin_defaults + ($user_defaults | to_entries | map(
      if .key == "cpu_min" then {key: "cpu_min_milli", value: (.value | tostring | gsub("m$";"") | tonumber)}
      elif .key == "cpu_max" then {key: "cpu_max_milli", value: (.value | tostring | gsub("m$";"") | tonumber)}
      elif .key == "memory_min" then {key: "memory_min_mi", value: (.value | tostring | gsub("Mi$";"") | tonumber)}
      elif .key == "memory_max" then {key: "memory_max_mi", value: (.value | tostring | gsub("Gi$";"") | tonumber * 1024)}
      else .
      end
    ) | from_entries)) as $merged_defaults |
    # Apply namespace override
    ($merged_defaults + ($ns_override | to_entries | map(
      if .key == "cpu_min" then {key: "cpu_min_milli", value: (.value | tostring | gsub("m$";"") | tonumber)}
      elif .key == "cpu_max" then {key: "cpu_max_milli", value: (.value | tostring | gsub("m$";"") | tonumber)}
      elif .key == "memory_min" then {key: "memory_min_mi", value: (.value | tostring | gsub("Mi$";"") | tonumber)}
      elif .key == "memory_max" then {key: "memory_max_mi", value: (.value | tostring | gsub("Gi$";"") | tonumber * 1024)}
      else .
      end
    ) | from_entries)) as $after_ns |
    # Apply workload override
    ($after_ns + ($wk_override | to_entries | map(
      if .key == "cpu_min" then {key: "cpu_min_milli", value: (.value | tostring | gsub("m$";"") | tonumber)}
      elif .key == "cpu_max" then {key: "cpu_max_milli", value: (.value | tostring | gsub("m$";"") | tonumber)}
      elif .key == "memory_min" then {key: "memory_min_mi", value: (.value | tostring | gsub("Mi$";"") | tonumber)}
      elif .key == "memory_max" then {key: "memory_max_mi", value: (.value | tostring | gsub("Gi$";"") | tonumber * 1024)}
      else .
      end
    ) | from_entries))
  '
}

validate_policy() {
  local eff_policy="$1"
  local valid
  valid=$(echo "$eff_policy" | jq '
    if .cpu_safety_factor < 1 then "cpu_safety_factor must be >= 1"
    elif .memory_safety_factor < 1 then "memory_safety_factor must be >= 1"
    elif .cpu_burst_multiplier < 1 then "cpu_burst_multiplier must be >= 1"
    elif .memory_burst_multiplier < 1 then "memory_burst_multiplier must be >= 1"
    elif .cpu_min_milli < 0 then "cpu_min_milli must be >= 0"
    elif .cpu_max_milli < .cpu_min_milli then "cpu_max_milli must be >= cpu_min_milli"
    elif .memory_min_mi < 0 then "memory_min_mi must be >= 0"
    elif .memory_max_mi < .memory_min_mi then "memory_max_mi must be >= memory_min_mi"
    elif .step_percent < 0 or .step_percent > 100 then "step_percent must be between 0 and 100"
    else "ok"
    end
  ' -r)
  if [ "$valid" != "ok" ]; then
    echo "{\"error\":\"policy validation failed: $valid\"}" >&2
    exit 1
  fi
}

###############################################################################
# Preflight
###############################################################################

preflight() {
  # Use the user's existing kubeconfig — kubectl reads $KUBECONFIG or ~/.kube/config by default
  local kc="${KUBECONFIG:-${HOME}/.kube/config}"
  if [ ! -f "$kc" ] && [ -z "${KUBECONFIG:-}" ]; then
    echo "{\"error\":\"no kubeconfig found at ${kc} — set KUBECONFIG or configure kubectl\"}" >&2
    exit 1
  fi

  local cluster_err
  if ! cluster_err=$(kubectl cluster-info --request-timeout=10s 2>&1); then
    echo "{\"error\":\"cannot connect to Kubernetes cluster: $(echo "$cluster_err" | head -1 | tr '"' "'")\"}" >&2
    exit 1
  fi
}

###############################################################################
# Discovery Functions
###############################################################################

discover_workloads() {
  local ns="$1"
  local workload_filter="$2"
  local label_sel="$3"

  if [ -n "$workload_filter" ]; then
    # Specific workload — parse kind/name
    local kind name
    if echo "$workload_filter" | grep -q '/'; then
      kind=$(echo "$workload_filter" | cut -d'/' -f1)
      name=$(echo "$workload_filter" | cut -d'/' -f2)
    else
      # Assume deployment if no kind specified
      kind="deployment"
      name="$workload_filter"
    fi

    local result
    if ! result=$(kubectl get "$kind" "$name" -n "$ns" -o json 2>&1); then
      echo "{\"error\":\"workload $kind/$name not found in namespace $ns: $result\"}" >&2
      exit 1
    fi
    # Wrap single workload into items array
    echo "$result" | jq '{items: [.]}'
  else
    # Discover all deployments and statefulsets
    local selector_args=""
    if [ -n "$label_sel" ]; then
      selector_args="-l $label_sel"
    fi

    local deploys sts
    # shellcheck disable=SC2086
    deploys=$(kubectl get deploy -n "$ns" $selector_args -o json 2>/dev/null || echo '{"items":[]}')
    # shellcheck disable=SC2086
    sts=$(kubectl get sts -n "$ns" $selector_args -o json 2>/dev/null || echo '{"items":[]}')

    # Merge items from both
    jq -n --argjson d "$deploys" --argjson s "$sts" '{items: ($d.items + $s.items)}'
  fi
}

extract_containers() {
  # Extract container resource specs from workload JSON
  local workload_json="$1"
  echo "$workload_json" | jq '[
    .items[] |
    . as $wl |
    {
      kind: .kind,
      name: .metadata.name,
      namespace: .metadata.namespace
    } as $meta |
    .spec.template.spec.containers[] |
    {
      workload_kind: ($meta.kind | ascii_downcase),
      workload_name: $meta.name,
      namespace: $meta.namespace,
      container: .name,
      current_cpu_request: (.resources.requests.cpu // "0"),
      current_cpu_limit: (.resources.limits.cpu // "0"),
      current_memory_request: (.resources.requests.memory // "0"),
      current_memory_limit: (.resources.limits.memory // "0")
    }
  ]'
}

###############################################################################
# Unit Conversion Helpers (via jq)
###############################################################################

# Convert CPU string (e.g., "500m", "1", "2.5") to millicores integer
cpu_to_milli() {
  local val="$1"
  echo "$val" | jq -Rr '
    if . == "0" or . == "" then 0
    elif test("m$") then rtrimstr("m") | tonumber
    else tonumber * 1000
    end | floor
  '
}

# Convert memory string (e.g., "512Mi", "1Gi", "1073741824") to MiB integer
memory_to_mi() {
  local val="$1"
  echo "$val" | jq -Rr '
    if . == "0" or . == "" then 0
    elif test("Gi$") then rtrimstr("Gi") | tonumber * 1024
    elif test("Mi$") then rtrimstr("Mi") | tonumber
    elif test("Ki$") then rtrimstr("Ki") | tonumber / 1024
    elif test("G$") then rtrimstr("G") | tonumber * 1000000000 / 1048576
    elif test("M$") then rtrimstr("M") | tonumber * 1000000 / 1048576
    elif test("K$") then rtrimstr("K") | tonumber * 1000 / 1048576
    else tonumber / 1048576
    end | floor
  '
}

###############################################################################
# Prometheus Metrics
###############################################################################

query_prom() {
  local promql="$1"
  local prom_url="${PROMETHEUS_URL:-}"

  if [ -z "$prom_url" ]; then
    return 1
  fi

  local auth_header=""
  if [ -n "${PROMETHEUS_TOKEN:-}" ]; then
    auth_header="Authorization: Bearer ${PROMETHEUS_TOKEN}"
  fi

  local response http_code body
  if [ -n "$auth_header" ]; then
    response=$(curl -s -w "\n%{http_code}" --max-time 30 \
      -G "${prom_url}/api/v1/query" \
      --data-urlencode "query=${promql}" \
      -H "$auth_header")
  else
    response=$(curl -s -w "\n%{http_code}" --max-time 30 \
      -G "${prom_url}/api/v1/query" \
      --data-urlencode "query=${promql}")
  fi

  http_code=$(echo "$response" | tail -1)
  body=$(echo "$response" | sed '$d')

  if [ "$http_code" -ne 200 ]; then
    echo "" # Return empty on failure
    return 1
  fi

  echo "$body"
}

get_metrics_prom() {
  local ns="$1"
  local pod_prefix="$2"
  local container="$3"
  local lookback_val="$4"

  # p95 CPU usage (cores)
  local cpu_query="quantile_over_time(0.95, rate(container_cpu_usage_seconds_total{namespace=\"${ns}\",pod=~\"${pod_prefix}.*\",container=\"${container}\"}[5m])[${lookback_val}:1m])"
  local cpu_result
  cpu_result=$(query_prom "$cpu_query" 2>/dev/null || echo "")

  # p95 Memory usage (bytes)
  local mem_query="quantile_over_time(0.95, container_memory_working_set_bytes{namespace=\"${ns}\",pod=~\"${pod_prefix}.*\",container=\"${container}\"}[${lookback_val}])"
  local mem_result
  mem_result=$(query_prom "$mem_query" 2>/dev/null || echo "")

  # Throttle ratio
  local throttle_query="rate(container_cpu_cfs_throttled_seconds_total{namespace=\"${ns}\",pod=~\"${pod_prefix}.*\",container=\"${container}\"}[${lookback_val}]) / rate(container_cpu_cfs_periods_total{namespace=\"${ns}\",pod=~\"${pod_prefix}.*\",container=\"${container}\"}[${lookback_val}])"
  local throttle_result
  throttle_result=$(query_prom "$throttle_query" 2>/dev/null || echo "")

  # OOM kills
  local oom_query="increase(kube_pod_container_status_restarts_total{namespace=\"${ns}\",pod=~\"${pod_prefix}.*\",container=\"${container}\",reason=\"OOMKilled\"}[${lookback_val}])"
  local oom_result
  oom_result=$(query_prom "$oom_query" 2>/dev/null || echo "")

  # Extract values, defaulting to empty on parse failure
  local p95_cpu_cores p95_mem_bytes throttle_ratio oom_kills

  p95_cpu_cores=$(echo "${cpu_result:-}" | jq -r '.data.result[0].value[1] // empty' 2>/dev/null || echo "")
  p95_mem_bytes=$(echo "${mem_result:-}" | jq -r '.data.result[0].value[1] // empty' 2>/dev/null || echo "")
  throttle_ratio=$(echo "${throttle_result:-}" | jq -r '.data.result[0].value[1] // empty' 2>/dev/null || echo "")
  oom_kills=$(echo "${oom_result:-}" | jq -r '.data.result[0].value[1] // empty' 2>/dev/null || echo "")

  # Convert to millicores and MiB
  local p95_cpu_milli="0"
  local p95_mem_mi="0"
  if [ -n "$p95_cpu_cores" ]; then
    p95_cpu_milli=$(echo "$p95_cpu_cores" | jq -r '. | tonumber * 1000 | floor')
  fi
  if [ -n "$p95_mem_bytes" ]; then
    p95_mem_mi=$(echo "$p95_mem_bytes" | jq -r '. | tonumber / 1048576 | floor')
  fi

  jq -n \
    --argjson cpu "$p95_cpu_milli" \
    --argjson mem "$p95_mem_mi" \
    --arg throttle "${throttle_ratio:-0}" \
    --arg oom "${oom_kills:-0}" \
    --arg source "prometheus" '{
      p95_cpu_milli: $cpu,
      p95_memory_mi: $mem,
      throttle_ratio: ($throttle | tonumber // 0),
      oom_kills: ($oom | tonumber | floor // 0),
      source: $source
    }'
}

###############################################################################
# kubectl top Fallback
###############################################################################

get_metrics_top() {
  local ns="$1"
  local pod_prefix="$2"
  local container="$3"

  ADVISORY_ONLY="true"

  local top_output
  if ! top_output=$(kubectl top pod -n "$ns" --containers 2>&1); then
    echo '{"error":"metrics-server unavailable: '"$(echo "$top_output" | head -1)"'"}' >&2
    return 1
  fi

  # Parse kubectl top output for matching pods/containers
  # Format: POD_NAME CONTAINER CPU(cores) MEMORY(bytes)
  local cpu_milli mem_mi
  cpu_milli=$(echo "$top_output" | grep -E "^${pod_prefix}" | awk -v c="$container" '$2 == c {print $3}' | head -1 || echo "")
  mem_mi=$(echo "$top_output" | grep -E "^${pod_prefix}" | awk -v c="$container" '$2 == c {print $4}' | head -1 || echo "")

  # Convert from kubectl top format
  if [ -z "$cpu_milli" ]; then
    cpu_milli="0"
  else
    cpu_milli=$(cpu_to_milli "$cpu_milli")
  fi
  if [ -z "$mem_mi" ]; then
    mem_mi="0"
  else
    mem_mi=$(memory_to_mi "$mem_mi")
  fi

  jq -n \
    --argjson cpu "$cpu_milli" \
    --argjson mem "$mem_mi" '{
      p95_cpu_milli: $cpu,
      p95_memory_mi: $mem,
      throttle_ratio: 0,
      oom_kills: 0,
      source: "metrics-server"
    }'
}

###############################################################################
# Compute Engine
###############################################################################

compute_recommendation() {
  local container_info="$1"
  local metrics="$2"
  local eff_policy="$3"

  jq -n --argjson c "$container_info" --argjson m "$metrics" --argjson p "$eff_policy" \
    --arg advisory "$ADVISORY_ONLY" '
    # Parse current values to millicores/MiB
    def parse_cpu:
      if . == "0" or . == "" or . == null then 0
      elif test("m$") then rtrimstr("m") | tonumber
      else tonumber * 1000
      end | floor;
    def parse_mem:
      if . == "0" or . == "" or . == null then 0
      elif test("Gi$") then rtrimstr("Gi") | tonumber * 1024
      elif test("Mi$") then rtrimstr("Mi") | tonumber
      elif test("Ki$") then rtrimstr("Ki") | tonumber / 1024
      else tonumber / 1048576
      end | floor;

    # Clamp helper
    def clamp(lo; hi): if . < lo then lo elif . > hi then hi else . end;

    # Round CPU to nearest 10m
    def round_cpu: ((. + 5) / 10 | floor) * 10 | if . < 10 then 10 else . end;

    # Round memory to nearest MiB (already integer)
    def round_mem: if . < 1 then 1 else . | floor end;

    ($c.current_cpu_request | parse_cpu) as $cur_cpu_req |
    ($c.current_cpu_limit | parse_cpu) as $cur_cpu_lim |
    ($c.current_memory_request | parse_mem) as $cur_mem_req |
    ($c.current_memory_limit | parse_mem) as $cur_mem_lim |

    $m.p95_cpu_milli as $p95_cpu |
    $m.p95_memory_mi as $p95_mem |

    # Step percent (doubled for advisory mode)
    (if $advisory == "true" then ($p.step_percent * 2) else $p.step_percent end) as $step |

    # Compute recommended CPU request
    ($p95_cpu * $p.cpu_safety_factor | round_cpu | clamp($p.cpu_min_milli; $p.cpu_max_milli)) as $rec_cpu_req |
    # Compute recommended CPU limit
    ($rec_cpu_req * $p.cpu_burst_multiplier | round_cpu | clamp($rec_cpu_req; $p.cpu_max_milli)) as $rec_cpu_lim |
    # Compute recommended memory request
    ($p95_mem * $p.memory_safety_factor | round_mem | clamp($p.memory_min_mi; $p.memory_max_mi)) as $rec_mem_req |
    # Compute recommended memory limit
    ($rec_mem_req * $p.memory_burst_multiplier | round_mem | clamp($rec_mem_req; $p.memory_max_mi)) as $rec_mem_lim |

    # Compute change percentages
    (if $cur_cpu_req > 0 then (($rec_cpu_req - $cur_cpu_req) / $cur_cpu_req * 100 | floor) else 100 end) as $cpu_req_pct |
    (if $cur_cpu_lim > 0 then (($rec_cpu_lim - $cur_cpu_lim) / $cur_cpu_lim * 100 | floor) else 100 end) as $cpu_lim_pct |
    (if $cur_mem_req > 0 then (($rec_mem_req - $cur_mem_req) / $cur_mem_req * 100 | floor) else 100 end) as $mem_req_pct |
    (if $cur_mem_lim > 0 then (($rec_mem_lim - $cur_mem_lim) / $cur_mem_lim * 100 | floor) else 100 end) as $mem_lim_pct |

    # Check step constraint — suppress if change is too small
    (if $cur_cpu_req > 0 then (($cpu_req_pct | fabs) >= $step) else true end) as $cpu_changed |
    (if $cur_mem_req > 0 then (($mem_req_pct | fabs) >= $step) else true end) as $mem_changed |
    ($cpu_changed or $mem_changed) as $has_recommendation |

    # Classification
    (if $p95_cpu == 0 and $p95_mem == 0 then "insufficient-data"
     elif $m.oom_kills > 0 then "limit-bound"
     elif $m.throttle_ratio > 0.1 then "limit-bound"
     elif ($cur_cpu_req > 0 and $cur_cpu_req > ($p95_cpu * $p.cpu_safety_factor * 2)) then "over-provisioned"
     elif ($cur_mem_req > 0 and $cur_mem_req > ($p95_mem * $p.memory_safety_factor * 2)) then "over-provisioned"
     elif ($cur_cpu_req > 0 and $cur_cpu_req < ($p95_cpu * 0.9)) then "under-provisioned"
     elif ($cur_mem_req > 0 and $cur_mem_req < ($p95_mem * 0.9)) then "under-provisioned"
     elif ($has_recommendation | not) then "right-sized"
     else "adjust"
     end) as $classification |

    {
      workload_kind: $c.workload_kind,
      workload_name: $c.workload_name,
      namespace: $c.namespace,
      container: $c.container,
      metrics_source: $m.source,
      classification: $classification,
      has_recommendation: $has_recommendation,
      cpu_request: {
        current_milli: $cur_cpu_req,
        recommended_milli: $rec_cpu_req,
        change_percent: $cpu_req_pct
      },
      cpu_limit: {
        current_milli: $cur_cpu_lim,
        recommended_milli: $rec_cpu_lim,
        change_percent: $cpu_lim_pct
      },
      memory_request: {
        current_mi: $cur_mem_req,
        recommended_mi: $rec_mem_req,
        change_percent: $mem_req_pct
      },
      memory_limit: {
        current_mi: $cur_mem_lim,
        recommended_mi: $rec_mem_lim,
        change_percent: $mem_lim_pct
      },
      throttle_ratio: $m.throttle_ratio,
      oom_kills: $m.oom_kills,
      advisory_only: ($advisory == "true")
    }
  '
}

###############################################################################
# Report Generation
###############################################################################

format_cpu() {
  # Convert millicores to display string
  local milli="$1"
  if [ "$milli" -ge 1000 ]; then
    echo "${milli}m"
  else
    echo "${milli}m"
  fi
}

format_mem() {
  # Convert MiB to display string
  local mi="$1"
  if [ "$mi" -ge 1024 ]; then
    local gi
    gi=$(echo "$mi" | jq -r '. / 1024 | . * 10 | floor / 10')
    echo "${gi}Gi"
  else
    echo "${mi}Mi"
  fi
}

generate_markdown_report() {
  local recommendations_file="$1"

  local count
  count=$(jq 'length' "$recommendations_file")

  if [ "$count" -eq 0 ]; then
    echo "# Rightsizing Report"
    echo ""
    echo "No workloads found or no recommendations to make."
    return
  fi

  local advisory_flag
  advisory_flag=$(jq -r '.[0].advisory_only' "$recommendations_file")

  echo "# Rightsizing Report"
  echo ""
  echo "**Namespace:** ${NAMESPACE}"
  echo "**Mode:** ${MODE}"
  echo "**Metrics source:** $(jq -r '.[0].metrics_source' "$recommendations_file")"
  echo "**Lookback:** ${LOOKBACK}"
  if [ "$advisory_flag" = "true" ]; then
    echo ""
    echo "> **Advisory only** — metrics-server provides point-in-time data only. Use Prometheus for production rightsizing."
  fi
  echo ""
  echo "| Workload | Container | Resource | Current | Recommended | Change | Classification |"
  echo "|----------|-----------|----------|---------|-------------|--------|----------------|"

  jq -r '.[] | select(.has_recommendation == true) |
    "\(.workload_kind)/\(.workload_name)|\(.container)|\(.cpu_request.current_milli)|\(.cpu_request.recommended_milli)|\(.cpu_request.change_percent)|\(.cpu_limit.current_milli)|\(.cpu_limit.recommended_milli)|\(.cpu_limit.change_percent)|\(.memory_request.current_mi)|\(.memory_request.recommended_mi)|\(.memory_request.change_percent)|\(.memory_limit.current_mi)|\(.memory_limit.recommended_mi)|\(.memory_limit.change_percent)|\(.classification)"
  ' "$recommendations_file" | while IFS='|' read -r wl ctr cur_cr rec_cr pct_cr cur_cl rec_cl pct_cl cur_mr rec_mr pct_mr cur_ml rec_ml pct_ml cls; do
    echo "| ${wl} | ${ctr} | CPU req | $(format_cpu "$cur_cr") | $(format_cpu "$rec_cr") | ${pct_cr}% | ${cls} |"
    echo "| ${wl} | ${ctr} | CPU lim | $(format_cpu "$cur_cl") | $(format_cpu "$rec_cl") | ${pct_cl}% | ${cls} |"
    echo "| ${wl} | ${ctr} | Mem req | $(format_mem "$cur_mr") | $(format_mem "$rec_mr") | ${pct_mr}% | ${cls} |"
    echo "| ${wl} | ${ctr} | Mem lim | $(format_mem "$cur_ml") | $(format_mem "$rec_ml") | ${pct_ml}% | ${cls} |"
  done

  # Summary of right-sized / insufficient-data
  local right_sized insufficient
  right_sized=$(jq '[.[] | select(.classification == "right-sized")] | length' "$recommendations_file")
  insufficient=$(jq '[.[] | select(.classification == "insufficient-data")] | length' "$recommendations_file")
  if [ "$right_sized" -gt 0 ] || [ "$insufficient" -gt 0 ]; then
    echo ""
    echo "**Summary:**"
    [ "$right_sized" -gt 0 ] && echo "- ${right_sized} container(s) are right-sized (no changes needed)"
    [ "$insufficient" -gt 0 ] && echo "- ${insufficient} container(s) have insufficient data for recommendations"
  fi
}

generate_json_report() {
  local recommendations_file="$1"

  jq '[.[] | {
    workload: "\(.workload_kind)/\(.workload_name)",
    container: .container,
    classification: .classification,
    advisory_only: .advisory_only,
    cpu_request: {
      current: "\(.cpu_request.current_milli)m",
      recommended: "\(.cpu_request.recommended_milli)m",
      change_percent: .cpu_request.change_percent
    },
    cpu_limit: {
      current: "\(.cpu_limit.current_milli)m",
      recommended: "\(.cpu_limit.recommended_milli)m",
      change_percent: .cpu_limit.change_percent
    },
    memory_request: {
      current: "\(.memory_request.current_mi)Mi",
      recommended: "\(.memory_request.recommended_mi)Mi",
      change_percent: .memory_request.change_percent
    },
    memory_limit: {
      current: "\(.memory_limit.current_mi)Mi",
      recommended: "\(.memory_limit.recommended_mi)Mi",
      change_percent: .memory_limit.change_percent
    },
    throttle_ratio: .throttle_ratio,
    oom_kills: .oom_kills
  }]' "$recommendations_file"
}

###############################################################################
# Patch Generation
###############################################################################

generate_patches() {
  local recommendations_file="$1"
  local output_dir="$2"

  jq -c '.[] | select(.has_recommendation == true)' "$recommendations_file" | while IFS= read -r rec; do
    local wl_kind wl_name ns ctr
    wl_kind=$(echo "$rec" | jq -r '.workload_kind')
    wl_name=$(echo "$rec" | jq -r '.workload_name')
    ns=$(echo "$rec" | jq -r '.namespace')
    ctr=$(echo "$rec" | jq -r '.container')

    local patch_file="${output_dir}/patch-${wl_kind}-${wl_name}.json"

    # Build strategic merge patch via jq
    local rec_cpu_req rec_cpu_lim rec_mem_req rec_mem_lim
    rec_cpu_req=$(echo "$rec" | jq -r '.cpu_request.recommended_milli')
    rec_cpu_lim=$(echo "$rec" | jq -r '.cpu_limit.recommended_milli')
    rec_mem_req=$(echo "$rec" | jq -r '.memory_request.recommended_mi')
    rec_mem_lim=$(echo "$rec" | jq -r '.memory_limit.recommended_mi')

    # If patch file already exists (multiple containers), merge
    if [ -f "$patch_file" ]; then
      local existing
      existing=$(cat "$patch_file")
      echo "$existing" | jq --arg ctr "$ctr" \
        --arg cpu_req "${rec_cpu_req}m" \
        --arg cpu_lim "${rec_cpu_lim}m" \
        --arg mem_req "${rec_mem_req}Mi" \
        --arg mem_lim "${rec_mem_lim}Mi" '
        .spec.template.spec.containers += [{
          name: $ctr,
          resources: {
            requests: {cpu: $cpu_req, memory: $mem_req},
            limits: {cpu: $cpu_lim, memory: $mem_lim}
          }
        }]
      ' > "$patch_file"
    else
      jq -n --arg ctr "$ctr" \
        --arg cpu_req "${rec_cpu_req}m" \
        --arg cpu_lim "${rec_cpu_lim}m" \
        --arg mem_req "${rec_mem_req}Mi" \
        --arg mem_lim "${rec_mem_lim}Mi" '{
          spec: {
            template: {
              spec: {
                containers: [{
                  name: $ctr,
                  resources: {
                    requests: {cpu: $cpu_req, memory: $mem_req},
                    limits: {cpu: $cpu_lim, memory: $mem_lim}
                  }
                }]
              }
            }
          }
        }' > "$patch_file"
    fi
  done
}

generate_rollback() {
  local ns="$1"
  local recommendations_file="$2"
  local rollback_dir="$3"

  mkdir -p "$rollback_dir"

  local timestamp
  timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  echo "[$timestamp] Rollback bundle created" > "${rollback_dir}/run.log"

  # Backup current specs for each unique workload
  jq -r '.[] | select(.has_recommendation == true) | "\(.workload_kind)/\(.workload_name)"' "$recommendations_file" | sort -u | while IFS= read -r wl_ref; do
    local kind name
    kind=$(echo "$wl_ref" | cut -d'/' -f1)
    name=$(echo "$wl_ref" | cut -d'/' -f2)

    local backup_file="${rollback_dir}/backup-${kind}-${name}.json"
    kubectl get "$kind" "$name" -n "$ns" -o json | jq '{
      spec: {
        template: {
          spec: {
            containers: [.spec.template.spec.containers[] | {
              name: .name,
              resources: .resources
            }]
          }
        }
      }
    }' > "$backup_file"

    # Generate rollback script
    local rollback_script="${rollback_dir}/rollback-${kind}-${name}.sh"
    local patch_content
    patch_content=$(cat "$backup_file")
    jq -n --arg kind "$kind" --arg name "$name" --arg ns "$ns" \
      --arg patch "$patch_content" '{
        command: "kubectl patch \($kind) \($name) -n \($ns) --type=strategic -p",
        patch: $patch
      }' > /dev/null  # Validate the jq works

    # Write rollback script without variable interpolation in the heredoc
    {
      echo '#!/usr/bin/env bash'
      echo 'set -euo pipefail'
      echo "kubectl patch ${kind} ${name} -n ${ns} --type=strategic -p '${patch_content}'"
    } > "$rollback_script"
    chmod +x "$rollback_script"

    echo "[$timestamp] Backed up $kind/$name" >> "${rollback_dir}/run.log"
  done
}

###############################################################################
# Apply Mode
###############################################################################

apply_patches() {
  local ns="$1"
  local recommendations_file="$2"
  local patch_dir="$3"
  local rollback_dir="$4"

  local timestamp
  timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

  jq -r '.[] | select(.has_recommendation == true) | "\(.workload_kind)/\(.workload_name)"' "$recommendations_file" | sort -u | while IFS= read -r wl_ref; do
    local kind name
    kind=$(echo "$wl_ref" | cut -d'/' -f1)
    name=$(echo "$wl_ref" | cut -d'/' -f2)

    local patch_file="${patch_dir}/patch-${kind}-${name}.json"
    if [ ! -f "$patch_file" ]; then
      echo "[$timestamp] SKIP: no patch file for $kind/$name" >> "${rollback_dir}/run.log"
      continue
    fi

    local patch_content
    patch_content=$(cat "$patch_file")

    echo "[$timestamp] Applying patch to $kind/$name in $ns" >> "${rollback_dir}/run.log"

    local apply_result
    if ! apply_result=$(kubectl patch "$kind" "$name" -n "$ns" --type=strategic -p "$patch_content" 2>&1); then
      echo "[$timestamp] FAILED: $kind/$name — $apply_result" >> "${rollback_dir}/run.log"
      echo "{\"error\":\"failed to patch $kind/$name: $apply_result\"}" >&2
      echo "Rollback available at: ${rollback_dir}/" >&2
      exit 1
    fi

    echo "[$timestamp] SUCCESS: $apply_result" >> "${rollback_dir}/run.log"

    # Verify rollout
    if ! kubectl rollout status "$kind/$name" -n "$ns" --timeout=120s 2>&1; then
      echo "[$timestamp] WARNING: rollout not yet complete for $kind/$name" >> "${rollback_dir}/run.log"
    fi
  done
}

###############################################################################
# YAML Output (for plan/apply modes)
###############################################################################

generate_yaml_output() {
  local patch_dir="$1"

  for patch_file in "${patch_dir}"/patch-*.json; do
    [ -f "$patch_file" ] || continue
    local basename_
    basename_=$(basename "$patch_file" .json)
    # Extract kind and name from filename: patch-<kind>-<name>.json
    local kind name
    kind=$(echo "$basename_" | sed 's/^patch-//' | cut -d'-' -f1)
    name=$(echo "$basename_" | sed 's/^patch-[^-]*-//')

    echo "---"
    # Convert patch JSON to YAML-like output via jq
    jq -r --arg kind "$kind" --arg name "$name" --arg ns "$NAMESPACE" '
      "apiVersion: apps/v1",
      "kind: \($kind | gsub("deployment";"Deployment") | gsub("statefulset";"StatefulSet") | gsub("sts";"StatefulSet"))",
      "metadata:",
      "  name: \($name)",
      "  namespace: \($ns)",
      "spec:",
      "  template:",
      "    spec:",
      "      containers:",
      (.spec.template.spec.containers[] |
        "        - name: \(.name)",
        "          resources:",
        "            requests:",
        "              cpu: \"\(.resources.requests.cpu)\"",
        "              memory: \"\(.resources.requests.memory)\"",
        "            limits:",
        "              cpu: \"\(.resources.limits.cpu)\"",
        "              memory: \"\(.resources.limits.memory)\""
      )
    ' "$patch_file"
  done
}

###############################################################################
# Main Orchestration
###############################################################################

main() {
  # Step 0: Preflight
  preflight

  # Load and validate policy
  local policy_json eff_policy
  policy_json=$(policy_load)

  # Step 1: Discover workloads
  local workloads_json containers_json
  workloads_json=$(discover_workloads "$NAMESPACE" "$WORKLOAD" "$LABEL_SELECTOR")

  local workload_count
  workload_count=$(echo "$workloads_json" | jq '.items | length')
  if [ "$workload_count" -eq 0 ]; then
    echo '{"error":"no workloads found matching criteria"}' >&2
    exit 1
  fi

  containers_json=$(extract_containers "$workloads_json")

  local container_count
  container_count=$(echo "$containers_json" | jq 'length')
  if [ "$container_count" -eq 0 ]; then
    echo '{"error":"no containers found in matched workloads"}' >&2
    exit 1
  fi

  # Step 2: Collect metrics and compute recommendations
  local recommendations="[]"

  local i=0
  while [ "$i" -lt "$container_count" ]; do
    local container_info
    container_info=$(echo "$containers_json" | jq ".[$i]")

    local wl_kind wl_name ctr_name
    wl_kind=$(echo "$container_info" | jq -r '.workload_kind')
    wl_name=$(echo "$container_info" | jq -r '.workload_name')
    ctr_name=$(echo "$container_info" | jq -r '.container')

    # Resolve policy for this workload
    local workload_key="${NAMESPACE}/${wl_name}"
    eff_policy=$(resolve_policy "$policy_json" "$NAMESPACE" "$workload_key")
    validate_policy "$eff_policy"

    # Collect metrics
    local metrics=""

    # Try Prometheus first
    if [ -n "${PROMETHEUS_URL:-}" ]; then
      metrics=$(get_metrics_prom "$NAMESPACE" "$wl_name" "$ctr_name" "$LOOKBACK" 2>/dev/null || echo "")
    fi

    # Fallback to kubectl top
    if [ -z "$metrics" ] || [ "$metrics" = "" ]; then
      metrics=$(get_metrics_top "$NAMESPACE" "$wl_name" "$ctr_name" 2>/dev/null || echo "")
    fi

    if [ -z "$metrics" ] || [ "$metrics" = "" ]; then
      # No metrics available — mark as insufficient data
      metrics='{"p95_cpu_milli":0,"p95_memory_mi":0,"throttle_ratio":0,"oom_kills":0,"source":"none"}'
    fi

    # Step 3: Compute recommendation
    local rec
    rec=$(compute_recommendation "$container_info" "$metrics" "$eff_policy")
    recommendations=$(echo "$recommendations" | jq --argjson r "$rec" '. + [$r]')

    i=$((i + 1))
  done

  # Block apply mode with metrics-server fallback
  if [ "$MODE" = "apply" ] && [ "$ADVISORY_ONLY" = "true" ]; then
    echo '{"error":"apply mode is blocked when using metrics-server fallback (insufficient data fidelity). Use Prometheus for apply mode."}' >&2
    exit 1
  fi

  # Save recommendations to temp file
  local rec_file="${TMPDIR_WORK}/recommendations.json"
  echo "$recommendations" > "$rec_file"

  # Check if there are any actionable recommendations
  local actionable_count
  actionable_count=$(jq '[.[] | select(.has_recommendation == true)] | length' "$rec_file")

  # Step 4: Generate output
  case "$MODE" in
    dry-run)
      case "$OUTPUT_FORMAT" in
        markdown) generate_markdown_report "$rec_file" ;;
        json) generate_json_report "$rec_file" ;;
        yaml)
          echo "# YAML output is only available in plan or apply mode"
          echo "# Showing markdown report instead"
          echo ""
          generate_markdown_report "$rec_file"
          ;;
      esac
      ;;
    plan)
      if [ "$actionable_count" -eq 0 ]; then
        echo "No actionable recommendations — all workloads are right-sized or have insufficient data."
        exit 0
      fi

      local patch_dir="${TMPDIR_WORK}/patches"
      mkdir -p "$patch_dir"
      generate_patches "$rec_file" "$patch_dir"

      case "$OUTPUT_FORMAT" in
        markdown)
          generate_markdown_report "$rec_file"
          echo ""
          echo "## Generated Patches"
          echo ""
          generate_yaml_output "$patch_dir"
          ;;
        json) generate_json_report "$rec_file" ;;
        yaml) generate_yaml_output "$patch_dir" ;;
      esac
      ;;
    apply)
      if [ "$actionable_count" -eq 0 ]; then
        echo "No actionable recommendations — all workloads are right-sized or have insufficient data."
        exit 0
      fi

      local ts
      ts=$(date -u +"%Y%m%dT%H%M%SZ")
      local rollback_dir="rollback-${ts}"
      local patch_dir="${TMPDIR_WORK}/patches"
      mkdir -p "$patch_dir"

      # Generate rollback bundle
      generate_rollback "$NAMESPACE" "$rec_file" "$rollback_dir"

      # Generate patches
      generate_patches "$rec_file" "$patch_dir"

      # Show report
      generate_markdown_report "$rec_file"
      echo ""
      echo "## Applying Patches"
      echo ""
      echo "Rollback bundle: \`${rollback_dir}/\`"
      echo ""

      # Apply
      apply_patches "$NAMESPACE" "$rec_file" "$patch_dir" "$rollback_dir"

      echo ""
      echo "Patches applied successfully. To rollback:"
      echo ""
      echo '```bash'
      echo "ls ${rollback_dir}/rollback-*.sh"
      echo '```'
      ;;
  esac
}

main
