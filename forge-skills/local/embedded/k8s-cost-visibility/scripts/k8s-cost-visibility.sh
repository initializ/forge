#!/usr/bin/env bash
# k8s-cost-visibility.sh — Estimate Kubernetes infrastructure costs by querying
# cluster node/pod data via kubectl, applying pricing models, and producing
# cost attribution reports.
#
# Usage: ./k8s-cost-visibility.sh '{"pricing_mode":"auto","group_by":"namespace"}'
#
# Requires: kubectl, jq, awk, bc, bash.
set -euo pipefail

# Flag to prevent duplicate error output (error_json sets this before exit)
__error_handled=0

# Catch unexpected exits and emit a JSON error so failures are never silent
trap '__exit_code=$?; if [ $__exit_code -ne 0 ] && [ "$__error_handled" -eq 0 ]; then __msg="{\"error\":\"script exited unexpectedly (code $__exit_code) at line ${LINENO:-unknown}\"}"; echo "$__msg" >&2; echo "$__msg"; fi' EXIT

###############################################################################
# Constants & Defaults
###############################################################################

# Default static pricing (based on AWS m5.xlarge on-demand US-East-1)
DEFAULT_CPU_HOURLY="0.031611"
DEFAULT_MEMORY_GIB_HOURLY="0.004237"
DEFAULT_CURRENCY="USD"
DEFAULT_STORAGE_GIB_MONTHLY="0.10"   # ~$0.10/GiB/month (Azure Standard SSD / AWS gp3 / GCP pd-balanced)
DEFAULT_LB_MONTHLY="18.25"           # ~$0.025/hr ≈ $18.25/month (AWS ALB / Azure Standard LB / GCP forwarding rule)

# Cache directory
CACHE_DIR="${TMPDIR:-/tmp}/k8s-cost-cache"

###############################################################################
# Helpers
###############################################################################

error_json() {
  local msg="$1"
  __error_handled=1
  echo "{\"error\":\"$msg\"}" >&2
  echo "{\"error\":\"$msg\"}"
  exit 1
}

json_safe() {
  # Escape a string for safe JSON embedding
  local s="$1"
  echo -n "$s" | jq -Rs '.'
}

###############################################################################
# Input Parsing & Validation
###############################################################################

INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  error_json "usage: k8s-cost-visibility.sh {\\\"pricing_mode\\\":\\\"auto\\\",\\\"group_by\\\":\\\"namespace\\\"}"
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  error_json "invalid JSON input"
fi

PRICING_MODE=$(echo "$INPUT" | jq -r '.pricing_mode // "auto"')
GROUP_BY=$(echo "$INPUT" | jq -r '.group_by // "namespace"')
LABEL_SELECTOR=$(echo "$INPUT" | jq -r '.label_selector // empty')
TOP_N=$(echo "$INPUT" | jq -r '.top // 0')
OUTPUT_FORMAT=$(echo "$INPUT" | jq -r '.output_format // "markdown"')
CACHE_TTL=$(echo "$INPUT" | jq -r '.cache_ttl // 300')
NAMESPACE=$(echo "$INPUT" | jq -r '.namespace // empty')

# Validate pricing_mode
# Normalize pricing_mode synonyms
case "$PRICING_MODE" in
  auto|default) PRICING_MODE="auto" ;;
  aws|amazon) PRICING_MODE="aws" ;;
  gcp|google) PRICING_MODE="gcp" ;;
  azure|az) PRICING_MODE="azure" ;;
  static|on_demand|on-demand|ondemand) PRICING_MODE="static" ;;
  custom:*)
    CUSTOM_PRICING_FILE="${PRICING_MODE#custom:}"
    if [ ! -f "$CUSTOM_PRICING_FILE" ]; then
      error_json "custom pricing file not found: $CUSTOM_PRICING_FILE"
    fi
    if ! jq empty "$CUSTOM_PRICING_FILE" 2>/dev/null; then
      error_json "invalid JSON in custom pricing file: $CUSTOM_PRICING_FILE"
    fi
    PRICING_MODE="custom"
    ;;
  *)
    # Unrecognized mode — fall back to auto-detection rather than failing
    PRICING_MODE="auto"
    ;;
esac

# Validate group_by
case "$GROUP_BY" in
  namespace|workload|node) ;;
  label:*|annotation:*)
    GROUP_KEY="${GROUP_BY#*:}"
    if [ -z "$GROUP_KEY" ]; then
      error_json "group_by '$GROUP_BY' requires a key (e.g., label:team)"
    fi
    ;;
  *)
    error_json "invalid group_by '$GROUP_BY': must be namespace, workload, node, label:<key>, or annotation:<key>"
    ;;
esac

# Validate output_format
case "$OUTPUT_FORMAT" in
  markdown|json) ;;
  *)
    error_json "invalid output_format '$OUTPUT_FORMAT': must be markdown or json"
    ;;
esac

# Validate top (must be non-negative integer)
if ! echo "$TOP_N" | grep -qE '^[0-9]+$'; then
  error_json "invalid top value '$TOP_N': must be a non-negative integer"
fi

# Validate cache_ttl (must be non-negative integer)
if ! echo "$CACHE_TTL" | grep -qE '^[0-9]+$'; then
  error_json "invalid cache_ttl value '$CACHE_TTL': must be a non-negative integer"
fi

###############################################################################
# Preflight
###############################################################################

preflight() {
  local kc="${KUBECONFIG:-${HOME}/.kube/config}"
  if [ ! -f "$kc" ] && [ -z "${KUBECONFIG:-}" ]; then
    error_json "no kubeconfig found at ${kc} — set KUBECONFIG or configure kubectl"
  fi

  local cluster_err
  if ! cluster_err=$(kubectl cluster-info --request-timeout=10s 2>&1); then
    error_json "cannot connect to Kubernetes cluster: $(echo "$cluster_err" | head -1 | tr '"' "'")"
  fi
}

###############################################################################
# Cache Functions
###############################################################################

cache_key() {
  local key="$1"
  echo "${CACHE_DIR}/${key}"
}

cache_get() {
  local key="$1"
  local file
  file=$(cache_key "$key")

  if [ "$CACHE_TTL" -eq 0 ]; then
    return 1
  fi

  if [ ! -f "$file" ]; then
    return 1
  fi

  # Check age — use stat with macOS/Linux fallback
  local file_age now file_mtime
  now=$(date +%s)
  file_mtime=$(stat -c %Y "$file" 2>/dev/null || stat -f %m "$file" 2>/dev/null || echo "0")
  file_age=$((now - file_mtime))

  if [ "$file_age" -gt "$CACHE_TTL" ]; then
    rm -f "$file"
    return 1
  fi

  cat "$file"
}

cache_set() {
  local key="$1"
  local value="$2"
  local file
  file=$(cache_key "$key")
  mkdir -p "$CACHE_DIR"
  echo "$value" > "$file"
}

###############################################################################
# Node Data Collection
###############################################################################

get_node_data() {
  local node_json
  node_json=$(kubectl get nodes -o json 2>/dev/null) || error_json "Failed to fetch nodes"

  echo "$node_json" | jq '[
    .items[] |
    {
      name: .metadata.name,
      labels: (.metadata.labels // {}),
      annotations: (.metadata.annotations // {}),
      instance_type: (
        (.metadata.labels // {})["node.kubernetes.io/instance-type"] //
        (.metadata.labels // {})["beta.kubernetes.io/instance-type"] //
        "unknown"
      ),
      region: (
        (.metadata.labels // {})["topology.kubernetes.io/region"] //
        (.metadata.labels // {})["failure-domain.beta.kubernetes.io/region"] //
        "unknown"
      ),
      allocatable_cpu_milli: (
        .status.allocatable.cpu |
        if . == null then 0
        else tostring |
          if test("m$") then rtrimstr("m") | tonumber
          else tonumber * 1000
          end
        end
      ),
      allocatable_memory_bytes: (
        .status.allocatable.memory |
        if . == null then 0
        else tostring |
          if test("Ki$") then rtrimstr("Ki") | tonumber * 1024
          elif test("Mi$") then rtrimstr("Mi") | tonumber * 1048576
          elif test("Gi$") then rtrimstr("Gi") | tonumber * 1073741824
          else tonumber
          end
        end
      )
    } |
    select(.allocatable_cpu_milli > 0)
  ]' || error_json "failed to parse node data — cluster may have unexpected node format"
}

###############################################################################
# Pricing Functions
###############################################################################

detect_cloud_provider() {
  # First, detect from node labels (most reliable — matches actual cluster provider)
  local provider_hint
  provider_hint=$(kubectl get nodes -o json 2>/dev/null | jq -r '
    .items[0].metadata.labels // {} |
    if has("kubernetes.azure.com/os-sku") or has("kubernetes.azure.com/cluster") then "azure"
    elif has("eks.amazonaws.com/nodegroup") or has("alpha.eksctl.io/cluster-name") then "aws"
    elif has("cloud.google.com/gke-nodepool") or has("cloud.google.com/machine-family") then "gcp"
    else "unknown"
    end
  ' 2>/dev/null || echo "unknown")

  case "$provider_hint" in
    aws)
      if command -v aws &>/dev/null; then echo "aws"; else echo "static"; fi
      ;;
    gcp)
      if command -v gcloud &>/dev/null; then echo "gcp"; else echo "static"; fi
      ;;
    azure)
      if command -v az &>/dev/null; then echo "azure"; else echo "static"; fi
      ;;
    *)
      # Fallback: check CLIs in order
      if command -v aws &>/dev/null; then echo "aws"
      elif command -v gcloud &>/dev/null; then echo "gcp"
      elif command -v az &>/dev/null; then echo "azure"
      else echo "static"
      fi
      ;;
  esac
}

get_static_pricing() {
  jq -n \
    --arg cpu "$DEFAULT_CPU_HOURLY" \
    --arg mem "$DEFAULT_MEMORY_GIB_HOURLY" \
    --arg currency "$DEFAULT_CURRENCY" '{
      cpu_hourly: ($cpu | tonumber),
      memory_gib_hourly: ($mem | tonumber),
      currency: $currency,
      source: "static"
    }'
}

get_custom_pricing() {
  jq '. + {source: "custom"}' "$CUSTOM_PRICING_FILE"
}

get_aws_pricing() {
  local instance_type="$1"
  local region="${AWS_REGION:-us-east-1}"
  local cache_result

  if cache_result=$(cache_get "aws-${region}-${instance_type}" 2>/dev/null); then
    echo "$cache_result"
    return
  fi

  local price_json
  if price_json=$(aws pricing get-products \
    --service-code AmazonEC2 \
    --region us-east-1 \
    --filters \
      "Type=TERM_MATCH,Field=instanceType,Value=${instance_type}" \
      "Type=TERM_MATCH,Field=location,Value=$(aws_region_to_location "$region")" \
      "Type=TERM_MATCH,Field=operatingSystem,Value=Linux" \
      "Type=TERM_MATCH,Field=tenancy,Value=Shared" \
      "Type=TERM_MATCH,Field=preInstalledSw,Value=NA" \
      "Type=TERM_MATCH,Field=capacitystatus,Value=Used" \
    --max-results 1 2>/dev/null); then

    local hourly_price
    hourly_price=$(echo "$price_json" | jq -r '
      .PriceList[0] // empty |
      fromjson |
      .terms.OnDemand | to_entries[0].value |
      .priceDimensions | to_entries[0].value |
      .pricePerUnit.USD // "0"
    ' 2>/dev/null || echo "0")

    if [ "$hourly_price" != "0" ] && [ -n "$hourly_price" ]; then
      local result
      result=$(jq -n --arg price "$hourly_price" --arg itype "$instance_type" '{
        instance_hourly: ($price | tonumber),
        instance_type: $itype,
        source: "aws"
      }')
      cache_set "aws-${region}-${instance_type}" "$result"
      echo "$result"
      return
    fi
  fi

  # Fallback to static
  get_static_pricing
}

aws_region_to_location() {
  local region="$1"
  case "$region" in
    us-east-1) echo "US East (N. Virginia)" ;;
    us-east-2) echo "US East (Ohio)" ;;
    us-west-1) echo "US West (N. California)" ;;
    us-west-2) echo "US West (Oregon)" ;;
    eu-west-1) echo "EU (Ireland)" ;;
    eu-west-2) echo "EU (London)" ;;
    eu-central-1) echo "EU (Frankfurt)" ;;
    ap-southeast-1) echo "Asia Pacific (Singapore)" ;;
    ap-northeast-1) echo "Asia Pacific (Tokyo)" ;;
    *) echo "US East (N. Virginia)" ;;
  esac
}

get_gcp_pricing() {
  local instance_type="$1"
  local project="${GCP_PROJECT:-}"
  local cache_result

  if cache_result=$(cache_get "gcp-${instance_type}" 2>/dev/null); then
    echo "$cache_result"
    return
  fi

  if [ -n "$project" ]; then
    local zone machine_info
    zone=$(gcloud config get-value compute/zone 2>/dev/null || echo "us-central1-a")

    if machine_info=$(gcloud compute machine-types describe "$instance_type" \
      --zone="$zone" --project="$project" --format=json 2>/dev/null); then

      local vcpus mem_mb
      vcpus=$(echo "$machine_info" | jq -r '.guestCpus // 0')
      mem_mb=$(echo "$machine_info" | jq -r '.memoryMb // 0')

      if [ "$vcpus" -gt 0 ]; then
        # Use static per-unit pricing with actual vCPU/memory counts
        local result
        result=$(jq -n \
          --argjson vcpus "$vcpus" \
          --argjson mem_mb "$mem_mb" \
          --argjson cpu_rate "$DEFAULT_CPU_HOURLY" \
          --argjson mem_rate "$DEFAULT_MEMORY_GIB_HOURLY" \
          --arg itype "$instance_type" '{
            instance_hourly: ($vcpus * $cpu_rate + ($mem_mb / 1024) * $mem_rate),
            instance_type: $itype,
            source: "gcp"
          }')
        cache_set "gcp-${instance_type}" "$result"
        echo "$result"
        return
      fi
    fi
  fi

  get_static_pricing
}

get_azure_pricing() {
  local instance_type="$1"
  local node_region="${2:-eastus}"
  local cache_result

  if cache_result=$(cache_get "azure-${node_region}-${instance_type}" 2>/dev/null); then
    echo "$cache_result"
    return
  fi

  # Auto-detect subscription if not set
  local sub="${AZURE_SUBSCRIPTION_ID:-}"
  if [ -z "$sub" ]; then
    sub=$(az account show --query 'id' -o tsv 2>/dev/null || echo "")
  fi

  if [ -n "$sub" ]; then
    local vm_info
    local size_name="$instance_type"

    if vm_info=$(az vm list-sizes --location "$node_region" --subscription "$sub" \
      --query "[?name=='$size_name']" -o json 2>/dev/null); then

      local vcpus mem_mb
      vcpus=$(echo "$vm_info" | jq -r '.[0].numberOfCores // 0')
      mem_mb=$(echo "$vm_info" | jq -r '.[0].memoryInMb // 0')

      if [ "$vcpus" -gt 0 ] 2>/dev/null; then
        local result
        result=$(jq -n \
          --argjson vcpus "$vcpus" \
          --argjson mem_mb "$mem_mb" \
          --argjson cpu_rate "$DEFAULT_CPU_HOURLY" \
          --argjson mem_rate "$DEFAULT_MEMORY_GIB_HOURLY" \
          --arg itype "$instance_type" '{
            instance_hourly: ($vcpus * $cpu_rate + ($mem_mb / 1024) * $mem_rate),
            instance_type: $itype,
            source: "azure"
          }')
        cache_set "azure-${node_region}-${instance_type}" "$result"
        echo "$result"
        return
      fi
    fi
  fi

  get_static_pricing
}

get_node_hourly_cost() {
  local node_json="$1"
  local mode="$2"

  local instance_type alloc_cpu_milli alloc_mem_bytes node_region
  instance_type=$(echo "$node_json" | jq -r '.instance_type')
  alloc_cpu_milli=$(echo "$node_json" | jq -r '.allocatable_cpu_milli')
  alloc_mem_bytes=$(echo "$node_json" | jq -r '.allocatable_memory_bytes')
  node_region=$(echo "$node_json" | jq -r '.region')

  case "$mode" in
    static)
      # Cost = vCPU_count * cpu_rate + GiB_count * mem_rate
      echo "$alloc_cpu_milli $alloc_mem_bytes" | awk \
        -v cpu_rate="$DEFAULT_CPU_HOURLY" \
        -v mem_rate="$DEFAULT_MEMORY_GIB_HOURLY" '{
          vcpus = $1 / 1000
          gib = $2 / 1073741824
          printf "%.6f\n", vcpus * cpu_rate + gib * mem_rate
        }'
      ;;
    custom)
      local cpu_hourly mem_hourly
      cpu_hourly=$(jq -r '.cpu_hourly // 0' "$CUSTOM_PRICING_FILE")
      mem_hourly=$(jq -r '.memory_gib_hourly // 0' "$CUSTOM_PRICING_FILE")
      echo "$alloc_cpu_milli $alloc_mem_bytes" | awk \
        -v cpu_rate="$cpu_hourly" \
        -v mem_rate="$mem_hourly" '{
          vcpus = $1 / 1000
          gib = $2 / 1073741824
          printf "%.6f\n", vcpus * cpu_rate + gib * mem_rate
        }'
      ;;
    aws)
      local pricing
      pricing=$(get_aws_pricing "$instance_type")
      local instance_hourly
      instance_hourly=$(echo "$pricing" | jq -r '.instance_hourly // 0')
      if [ "$instance_hourly" != "0" ] && [ -n "$instance_hourly" ]; then
        echo "$instance_hourly"
      else
        # Fallback to static
        echo "$alloc_cpu_milli $alloc_mem_bytes" | awk \
          -v cpu_rate="$DEFAULT_CPU_HOURLY" \
          -v mem_rate="$DEFAULT_MEMORY_GIB_HOURLY" '{
            vcpus = $1 / 1000
            gib = $2 / 1073741824
            printf "%.6f\n", vcpus * cpu_rate + gib * mem_rate
          }'
      fi
      ;;
    gcp)
      local pricing
      pricing=$(get_gcp_pricing "$instance_type")
      local instance_hourly
      instance_hourly=$(echo "$pricing" | jq -r '.instance_hourly // 0')
      if [ "$instance_hourly" != "0" ] && [ -n "$instance_hourly" ]; then
        echo "$instance_hourly"
      else
        echo "$alloc_cpu_milli $alloc_mem_bytes" | awk \
          -v cpu_rate="$DEFAULT_CPU_HOURLY" \
          -v mem_rate="$DEFAULT_MEMORY_GIB_HOURLY" '{
            vcpus = $1 / 1000
            gib = $2 / 1073741824
            printf "%.6f\n", vcpus * cpu_rate + gib * mem_rate
          }'
      fi
      ;;
    azure)
      local pricing
      pricing=$(get_azure_pricing "$instance_type" "$node_region")
      local instance_hourly
      instance_hourly=$(echo "$pricing" | jq -r '.instance_hourly // 0')
      if [ "$instance_hourly" != "0" ] && [ -n "$instance_hourly" ]; then
        echo "$instance_hourly"
      else
        echo "$alloc_cpu_milli $alloc_mem_bytes" | awk \
          -v cpu_rate="$DEFAULT_CPU_HOURLY" \
          -v mem_rate="$DEFAULT_MEMORY_GIB_HOURLY" '{
            vcpus = $1 / 1000
            gib = $2 / 1073741824
            printf "%.6f\n", vcpus * cpu_rate + gib * mem_rate
          }'
      fi
      ;;
  esac
}

###############################################################################
# Pod Data Collection
###############################################################################

get_pod_data() {
  local POD_DATA
  local ns_flag="--all-namespaces"
  [ -n "$NAMESPACE" ] && ns_flag="-n $NAMESPACE"
  if [[ -n "$LABEL_SELECTOR" ]]; then
    POD_DATA=$(kubectl get pods $ns_flag -l "$LABEL_SELECTOR" -o json 2>/dev/null) || error_json "Failed to fetch pods"
  else
    POD_DATA=$(kubectl get pods $ns_flag -o json 2>/dev/null) || error_json "Failed to fetch pods"
  fi

  echo "$POD_DATA" | jq '[
    .items[] |
    select(.status.phase == "Running") |
    {
      name: .metadata.name,
      namespace: .metadata.namespace,
      node_name: (.spec.nodeName // "unscheduled"),
      labels: (.metadata.labels // {}),
      annotations: (.metadata.annotations // {}),
      owner_kind: ((.metadata.ownerReferences // [])[0].kind // "standalone"),
      owner_name: ((.metadata.ownerReferences // [])[0].name // .metadata.name),
      cpu_request_milli: (
        [.spec.containers[].resources.requests.cpu // "0" |
          tostring |
          if test("m$") then rtrimstr("m") | tonumber
          elif . == "0" then 0
          else tonumber * 1000
          end
        ] | add // 0
      ),
      memory_request_bytes: (
        [.spec.containers[].resources.requests.memory // "0" |
          tostring |
          if test("Ki$") then rtrimstr("Ki") | tonumber * 1024
          elif test("Mi$") then rtrimstr("Mi") | tonumber * 1048576
          elif test("Gi$") then rtrimstr("Gi") | tonumber * 1073741824
          elif . == "0" then 0
          else tonumber
          end
        ] | add // 0
      )
    }
  ]' || error_json "failed to parse pod data — cluster may have unexpected pod format"
}

###############################################################################
# Storage & LoadBalancer Data Collection
###############################################################################

get_pvc_data() {
  local pvc_json
  local ns_flag="--all-namespaces"
  [ -n "$NAMESPACE" ] && ns_flag="-n $NAMESPACE"
  pvc_json=$(kubectl get pvc $ns_flag -o json 2>/dev/null) || { echo "[]"; return; }

  echo "$pvc_json" | jq '[
    .items[] |
    {
      namespace: .metadata.namespace,
      name: .metadata.name,
      storage_class: (.spec.storageClassName // "default"),
      volume_name: (.spec.volumeName // ""),
      capacity_bytes: (
        (.status.capacity.storage // .spec.resources.requests.storage // "0") |
        tostring |
        if test("Ti$") then rtrimstr("Ti") | tonumber * 1099511627776
        elif test("Gi$") then rtrimstr("Gi") | tonumber * 1073741824
        elif test("Mi$") then rtrimstr("Mi") | tonumber * 1048576
        elif test("Ki$") then rtrimstr("Ki") | tonumber * 1024
        elif . == "0" then 0
        else tonumber
        end
      )
    }
  ]'
}

get_unbound_pvs() {
  local pv_json
  pv_json=$(kubectl get pv -o json 2>/dev/null) || { echo "[]"; return; }

  echo "$pv_json" | jq '[
    .items[] |
    select(.status.phase != "Bound") |
    {
      name: .metadata.name,
      storage_class: (.spec.storageClassName // "default"),
      reclaim_policy: (.spec.persistentVolumeReclaimPolicy // "Delete"),
      phase: .status.phase,
      capacity_bytes: (
        (.spec.capacity.storage // "0") |
        tostring |
        if test("Ti$") then rtrimstr("Ti") | tonumber * 1099511627776
        elif test("Gi$") then rtrimstr("Gi") | tonumber * 1073741824
        elif test("Mi$") then rtrimstr("Mi") | tonumber * 1048576
        elif test("Ki$") then rtrimstr("Ki") | tonumber * 1024
        elif . == "0" then 0
        else tonumber
        end
      )
    }
  ]'
}

compute_storage_costs() {
  local pvc_data="$1"
  local storage_rate="$DEFAULT_STORAGE_GIB_MONTHLY"

  if [ "$PRICING_MODE" = "custom" ] && [ -n "${CUSTOM_PRICING_FILE:-}" ]; then
    local custom_rate
    custom_rate=$(jq -r '.storage_gib_monthly // empty' "$CUSTOM_PRICING_FILE" 2>/dev/null || true)
    if [ -n "$custom_rate" ]; then
      storage_rate="$custom_rate"
    fi
  fi

  echo "$pvc_data" | jq --arg rate "$storage_rate" '[
    .[] |
    {
      namespace: .namespace,
      pvc_name: .name,
      storage_class: .storage_class,
      capacity_gib: (.capacity_bytes / 1073741824),
      monthly_cost: ((.capacity_bytes / 1073741824) * ($rate | tonumber))
    }
  ]'
}

get_lb_services() {
  local svc_json
  local ns_flag="--all-namespaces"
  [ -n "$NAMESPACE" ] && ns_flag="-n $NAMESPACE"
  svc_json=$(kubectl get svc $ns_flag -o json 2>/dev/null) || { echo "[]"; return; }

  echo "$svc_json" | jq '[
    .items[] |
    select(.spec.type == "LoadBalancer") |
    {
      namespace: .metadata.namespace,
      name: .metadata.name,
      external_ip: (
        (.status.loadBalancer.ingress // [])[0] |
        if . == null then "pending"
        elif .ip then .ip
        elif .hostname then .hostname
        else "pending"
        end
      ),
      port_count: (.spec.ports | length),
      created: .metadata.creationTimestamp
    }
  ]'
}

compute_lb_costs() {
  local lb_data="$1"
  local lb_rate="$DEFAULT_LB_MONTHLY"

  if [ "$PRICING_MODE" = "custom" ] && [ -n "${CUSTOM_PRICING_FILE:-}" ]; then
    local custom_rate
    custom_rate=$(jq -r '.lb_monthly // empty' "$CUSTOM_PRICING_FILE" 2>/dev/null || true)
    if [ -n "$custom_rate" ]; then
      lb_rate="$custom_rate"
    fi
  fi

  echo "$lb_data" | jq --arg rate "$lb_rate" '[
    .[] |
    {
      namespace: .namespace,
      service_name: .name,
      external_ip: .external_ip,
      port_count: .port_count,
      monthly_cost: ($rate | tonumber)
    }
  ]'
}

###############################################################################
# Cost Computation
###############################################################################

compute_costs() {
  local node_data="$1"
  local pod_data="$2"
  local pricing_mode="$3"

  # Build node cost map using jq (safe JSON construction)
  local node_costs="{}"
  local node_count
  node_count=$(echo "$node_data" | jq 'length')

  local i=0
  while [ "$i" -lt "$node_count" ]; do
    local node_info node_name hourly_cost
    node_info=$(echo "$node_data" | jq ".[$i]")
    node_name=$(echo "$node_info" | jq -r '.name')
    hourly_cost=$(get_node_hourly_cost "$node_info" "$pricing_mode" 2>/dev/null || echo "")

    # Guard against empty or non-numeric hourly_cost
    if [ -z "$hourly_cost" ] || ! echo "$hourly_cost" | grep -qE '^[0-9.]+$'; then
      hourly_cost="0"
    fi

    local alloc_cpu alloc_mem
    alloc_cpu=$(echo "$node_info" | jq '.allocatable_cpu_milli // 0')
    alloc_mem=$(echo "$node_info" | jq '.allocatable_memory_bytes // 0')

    node_costs=$(echo "$node_costs" | jq \
      --arg name "$node_name" \
      --argjson cost "$hourly_cost" \
      --argjson cpu "$alloc_cpu" \
      --argjson mem "$alloc_mem" \
      '. + {($name): {hourly_cost: $cost, alloc_cpu_milli: $cpu, alloc_mem_bytes: $mem}}')

    i=$((i + 1))
  done

  # Compute per-pod costs
  echo "$pod_data" | jq --argjson nodes "$node_costs" '[
    .[] |
    . as $pod |
    ($nodes[$pod.node_name] // null) as $node |
    if $node == null then
      . + {hourly_cost: 0, monthly_cost: 0, cost_source: "unscheduled"}
    else
      (
        if $node.alloc_cpu_milli > 0 then
          ($pod.cpu_request_milli / $node.alloc_cpu_milli)
        else 0 end
      ) as $cpu_fraction |
      (
        if $node.alloc_mem_bytes > 0 then
          ($pod.memory_request_bytes / $node.alloc_mem_bytes)
        else 0 end
      ) as $mem_fraction |
      (($cpu_fraction + $mem_fraction) / 2 * $node.hourly_cost) as $hourly |
      . + {
        hourly_cost: $hourly,
        monthly_cost: ($hourly * 730),
        cost_source: "computed",
        cpu_fraction: $cpu_fraction,
        mem_fraction: $mem_fraction,
        node_hourly_cost: $node.hourly_cost
      }
    end
  ]'
}

###############################################################################
# Grouping & Aggregation
###############################################################################

group_costs() {
  local pod_costs="$1"
  local group_by="$2"
  local storage_costs="${3:-[]}"
  local lb_costs="${4:-[]}"

  case "$group_by" in
    namespace)
      echo "$pod_costs" | jq --argjson sc "$storage_costs" --argjson lc "$lb_costs" '
        # Build storage and LB lookup maps by namespace
        (if ($sc | length) > 0 then ($sc | group_by(.namespace) | map({key: .[0].namespace, value: {storage_gib: ([.[].capacity_gib] | add // 0), storage_monthly_cost: ([.[].monthly_cost] | add // 0)}}) | from_entries) else {} end) as $storage_map |
        (if ($lc | length) > 0 then ($lc | group_by(.namespace) | map({key: .[0].namespace, value: {lb_count: length, lb_monthly_cost: ([.[].monthly_cost] | add // 0)}}) | from_entries) else {} end) as $lb_map |
        # Union all namespaces from pods, storage, and LB sources
        (([.[] | .namespace] + [$sc[] | .namespace] + [$lc[] | .namespace]) | unique) as $all_ns |
        # Build pod data lookup map by namespace
        (if length > 0 then (group_by(.namespace) | map({key: .[0].namespace, value: .}) | from_entries) else {} end) as $pod_map |
        [
          $all_ns[] | . as $ns |
          ($pod_map[$ns] // []) as $pods |
          {
            group_key: $ns,
            pod_count: ($pods | length),
            total_cpu_milli: ([$pods[].cpu_request_milli] | add // 0),
            total_memory_bytes: ([$pods[].memory_request_bytes] | add // 0),
            hourly_cost: ([$pods[].hourly_cost] | add // 0),
            monthly_cost: ([$pods[].monthly_cost] | add // 0),
            storage_gib: (($storage_map[$ns].storage_gib) // 0),
            storage_monthly_cost: (($storage_map[$ns].storage_monthly_cost) // 0),
            lb_count: (($lb_map[$ns].lb_count) // 0),
            lb_monthly_cost: (($lb_map[$ns].lb_monthly_cost) // 0)
          }
        ] | sort_by(-(.monthly_cost + .storage_monthly_cost + .lb_monthly_cost))'
      ;;
    workload)
      echo "$pod_costs" | jq '[
        group_by(.namespace + "/" + .owner_kind + "/" + .owner_name)[] |
        {
          group_key: (.[0].namespace + "/" + .[0].owner_kind + "/" + .[0].owner_name),
          namespace: .[0].namespace,
          kind: .[0].owner_kind,
          workload_name: .[0].owner_name,
          pod_count: length,
          total_cpu_milli: ([.[].cpu_request_milli] | add // 0),
          total_memory_bytes: ([.[].memory_request_bytes] | add // 0),
          hourly_cost: ([.[].hourly_cost] | add // 0),
          monthly_cost: ([.[].monthly_cost] | add // 0)
        }
      ] | sort_by(-.monthly_cost)'
      ;;
    node)
      echo "$pod_costs" | jq '[
        group_by(.node_name)[] |
        {
          group_key: .[0].node_name,
          pod_count: length,
          total_cpu_milli: ([.[].cpu_request_milli] | add // 0),
          total_memory_bytes: ([.[].memory_request_bytes] | add // 0),
          hourly_cost: ([.[].hourly_cost] | add // 0),
          monthly_cost: ([.[].monthly_cost] | add // 0),
          node_hourly_cost: (.[0].node_hourly_cost // 0)
        }
      ] | sort_by(-.monthly_cost)'
      ;;
    label:*)
      local key="${group_by#label:}"
      echo "$pod_costs" | jq --arg key "$key" '[
        group_by(.labels[$key] // "unset")[] |
        {
          group_key: (.[0].labels[$key] // "unset"),
          label_key: $key,
          pod_count: length,
          total_cpu_milli: ([.[].cpu_request_milli] | add // 0),
          total_memory_bytes: ([.[].memory_request_bytes] | add // 0),
          hourly_cost: ([.[].hourly_cost] | add // 0),
          monthly_cost: ([.[].monthly_cost] | add // 0)
        }
      ] | sort_by(-.monthly_cost)'
      ;;
    annotation:*)
      local key="${group_by#annotation:}"
      echo "$pod_costs" | jq --arg key "$key" '[
        group_by(.annotations[$key] // "unset")[] |
        {
          group_key: (.[0].annotations[$key] // "unset"),
          annotation_key: $key,
          pod_count: length,
          total_cpu_milli: ([.[].cpu_request_milli] | add // 0),
          total_memory_bytes: ([.[].memory_request_bytes] | add // 0),
          hourly_cost: ([.[].hourly_cost] | add // 0),
          monthly_cost: ([.[].monthly_cost] | add // 0)
        }
      ] | sort_by(-.monthly_cost)'
      ;;
  esac
}

apply_top_n() {
  local data="$1"
  local top_n="$2"

  if [ "$top_n" -gt 0 ]; then
    echo "$data" | jq --argjson n "$top_n" '.[:$n]'
  else
    echo "$data"
  fi
}

###############################################################################
# Report Generation
###############################################################################

format_cost() {
  # Format a decimal cost value to 2 decimal places
  local val="$1"
  printf "%.2f" "$val"
}

format_cpu_display() {
  local milli="$1"
  if [ "$milli" -ge 1000 ] 2>/dev/null; then
    echo "$(echo "$milli" | awk '{printf "%.1f", $1/1000}') vCPU"
  else
    echo "${milli}m"
  fi
}

format_memory_display() {
  local bytes="$1"
  local gib
  gib=$(echo "$bytes" | awk '{printf "%.1f", $1/1073741824}')
  if echo "$gib" | awk '{exit ($1 >= 1.0) ? 0 : 1}'; then
    echo "${gib} GiB"
  else
    local mib
    mib=$(echo "$bytes" | awk '{printf "%.0f", $1/1048576}')
    echo "${mib} MiB"
  fi
}

generate_markdown() {
  local grouped_data="$1"
  local group_by="$2"
  local pricing_source="$3"
  local storage_costs="${4:-[]}"
  local lb_costs="${5:-[]}"
  local unbound_pvs="${6:-[]}"
  local entry_count

  entry_count=$(echo "$grouped_data" | jq 'length')
  local total_compute_monthly total_storage_monthly total_lb_monthly
  total_compute_monthly=$(echo "$grouped_data" | jq '[.[].monthly_cost] | add // 0')
  total_storage_monthly=$(echo "$storage_costs" | jq '[.[].monthly_cost] | add // 0')
  total_lb_monthly=$(echo "$lb_costs" | jq '[.[].monthly_cost] | add // 0')
  local total_monthly total_hourly
  total_monthly=$(echo "$total_compute_monthly $total_storage_monthly $total_lb_monthly" | awk '{printf "%.6f", $1 + $2 + $3}')
  total_hourly=$(echo "$total_monthly" | awk '{printf "%.6f", $1 / 730}')

  echo "# Kubernetes Cost Report"
  echo ""
  echo "**Grouped by:** ${group_by}"
  echo "**Pricing source:** ${pricing_source}"
  echo "**Currency:** ${DEFAULT_CURRENCY}"
  if [ -n "$NAMESPACE" ]; then
    echo "**Namespace:** ${NAMESPACE}"
  fi
  if [ -n "$LABEL_SELECTOR" ]; then
    echo "**Label filter:** ${LABEL_SELECTOR}"
  fi
  echo "**Total hourly:** \$$(format_cost "$total_hourly")"
  echo "**Total monthly (730h):** \$$(format_cost "$total_monthly")"
  echo ""

  case "$group_by" in
    namespace)
      echo "| Namespace | Pods | CPU Req | Mem Req | Compute \$/mo | Storage \$/mo | LB \$/mo | Total \$/mo | % of Total |"
      echo "|-----------|------|---------|---------|-------------|-------------|---------|-----------|------------|"
      echo "$grouped_data" | jq -r --argjson total "$total_monthly" '.[] |
        (.monthly_cost + .storage_monthly_cost + .lb_monthly_cost) as $row_total |
        "\(.group_key)\t\(.pod_count)\t\(.total_cpu_milli)\t\(.total_memory_bytes)\t\(.monthly_cost)\t\(.storage_monthly_cost)\t\(.lb_monthly_cost)\t\($row_total)\t\(if $total > 0 then ($row_total / $total * 100) else 0 end)"
      ' | while IFS=$'\t' read -r gk pods cpu mem compute storage lb total pct; do
        echo "| ${gk} | ${pods} | $(format_cpu_display "$cpu") | $(format_memory_display "$mem") | \$$(format_cost "$compute") | \$$(format_cost "$storage") | \$$(format_cost "$lb") | \$$(format_cost "$total") | $(printf "%.1f" "$pct")% |"
      done
      ;;
    workload)
      echo "| Workload | Namespace | Pods | CPU Requests | Memory Requests | Monthly Cost | % of Total |"
      echo "|----------|-----------|------|-------------|-----------------|-------------|------------|"
      echo "$grouped_data" | jq -r --argjson total "$total_monthly" '.[] |
        "\(.kind)/\(.workload_name)\t\(.namespace)\t\(.pod_count)\t\(.total_cpu_milli)\t\(.total_memory_bytes)\t\(.monthly_cost)\t\(if $total > 0 then (.monthly_cost / $total * 100) else 0 end)"
      ' | while IFS=$'\t' read -r wl ns pods cpu mem monthly pct; do
        echo "| ${wl} | ${ns} | ${pods} | $(format_cpu_display "$cpu") | $(format_memory_display "$mem") | \$$(format_cost "$monthly") | $(printf "%.1f" "$pct")% |"
      done
      ;;
    node)
      echo "| Node | Pods | CPU Requests | Memory Requests | Node Cost/hr | Pod Cost/mo | Utilization |"
      echo "|------|------|-------------|-----------------|-------------|-------------|-------------|"
      echo "$grouped_data" | jq -r '.[] |
        "\(.group_key)\t\(.pod_count)\t\(.total_cpu_milli)\t\(.total_memory_bytes)\t\(.node_hourly_cost)\t\(.monthly_cost)\t\(if .node_hourly_cost > 0 then (.hourly_cost / .node_hourly_cost * 100) else 0 end)"
      ' | while IFS=$'\t' read -r node pods cpu mem node_hr monthly util; do
        echo "| ${node} | ${pods} | $(format_cpu_display "$cpu") | $(format_memory_display "$mem") | \$$(format_cost "$node_hr") | \$$(format_cost "$monthly") | $(printf "%.1f" "$util")% |"
      done
      ;;
    label:*|annotation:*)
      local dim_label
      dim_label=$(echo "$group_by" | cut -d: -f1)
      local dim_key
      dim_key=$(echo "$group_by" | cut -d: -f2)
      echo "| ${dim_label}:${dim_key} | Pods | CPU Requests | Memory Requests | Monthly Cost | % of Total |"
      echo "|$(printf '%0.s-' {1..20})|------|-------------|-----------------|-------------|------------|"
      echo "$grouped_data" | jq -r --argjson total "$total_monthly" '.[] |
        "\(.group_key)\t\(.pod_count)\t\(.total_cpu_milli)\t\(.total_memory_bytes)\t\(.monthly_cost)\t\(if $total > 0 then (.monthly_cost / $total * 100) else 0 end)"
      ' | while IFS=$'\t' read -r gk pods cpu mem monthly pct; do
        echo "| ${gk} | ${pods} | $(format_cpu_display "$cpu") | $(format_memory_display "$mem") | \$$(format_cost "$monthly") | $(printf "%.1f" "$pct")% |"
      done
      ;;
  esac

  if [ "$TOP_N" -gt 0 ] && [ "$entry_count" -eq "$TOP_N" ]; then
    echo ""
    echo "_Showing top ${TOP_N} entries by cost._"
  fi

  # LoadBalancer Services section
  local lb_count
  lb_count=$(echo "$lb_costs" | jq 'length')
  if [ "$lb_count" -gt 0 ]; then
    echo ""
    echo "## LoadBalancer Services"
    echo ""
    echo "| Namespace | Service | External IP | Monthly Cost |"
    echo "|-----------|---------|-------------|-------------|"
    echo "$lb_costs" | jq -r '.[] |
      "\(.namespace)\t\(.service_name)\t\(.external_ip)\t\(.monthly_cost)"
    ' | while IFS=$'\t' read -r ns svc ip cost; do
      echo "| ${ns} | ${svc} | ${ip} | \$$(format_cost "$cost") |"
    done
    echo ""
    echo "**Total LoadBalancer cost:** \$$(format_cost "$total_lb_monthly")/month"
  fi

  # Unbound Persistent Volumes (Waste) section
  local unbound_count
  unbound_count=$(echo "$unbound_pvs" | jq 'length')
  if [ "$unbound_count" -gt 0 ]; then
    local total_pv_waste
    total_pv_waste=$(echo "$unbound_pvs" | jq '[.[].monthly_waste] | add // 0')
    echo ""
    echo "## Unbound Persistent Volumes (Waste)"
    echo ""
    echo "| PV Name | Capacity | Storage Class | Reclaim Policy | Phase | Est. Monthly Waste |"
    echo "|---------|----------|---------------|----------------|-------|--------------------|"
    echo "$unbound_pvs" | jq -r '.[] |
      "\(.name)\t\(.capacity_gib)\t\(.storage_class)\t\(.reclaim_policy)\t\(.phase)\t\(.monthly_waste)"
    ' | while IFS=$'\t' read -r name cap sc rp phase waste; do
      echo "| ${name} | $(printf "%.1f" "$cap") GiB | ${sc} | ${rp} | ${phase} | \$$(format_cost "$waste") |"
    done
    echo ""
    echo "**Total estimated waste:** \$$(format_cost "$total_pv_waste")/month"
  fi
}

generate_json_output() {
  local grouped_data="$1"
  local group_by="$2"
  local pricing_source="$3"
  local storage_costs="${4:-[]}"
  local lb_costs="${5:-[]}"
  local unbound_pvs="${6:-[]}"

  local total_compute_monthly total_storage_monthly total_lb_monthly
  total_compute_monthly=$(echo "$grouped_data" | jq '[.[].monthly_cost] | add // 0')
  total_storage_monthly=$(echo "$storage_costs" | jq '[.[].monthly_cost] | add // 0')
  total_lb_monthly=$(echo "$lb_costs" | jq '[.[].monthly_cost] | add // 0')
  local total_monthly total_hourly
  total_monthly=$(echo "$total_compute_monthly $total_storage_monthly $total_lb_monthly" | awk '{printf "%.6f", $1 + $2 + $3}')
  total_hourly=$(echo "$total_monthly" | awk '{printf "%.6f", $1 / 730}')

  jq -n \
    --arg group_by "$group_by" \
    --arg pricing_source "$pricing_source" \
    --arg currency "$DEFAULT_CURRENCY" \
    --arg namespace "${NAMESPACE:-}" \
    --arg label_selector "$LABEL_SELECTOR" \
    --argjson total_hourly "$total_hourly" \
    --argjson total_monthly "$total_monthly" \
    --argjson total_compute_monthly "$total_compute_monthly" \
    --argjson total_storage_monthly "$total_storage_monthly" \
    --argjson total_lb_monthly "$total_lb_monthly" \
    --argjson top_n "$TOP_N" \
    --argjson entries "$grouped_data" \
    --argjson storage_costs "$storage_costs" \
    --argjson lb_services "$lb_costs" \
    --argjson unbound_pvs "$unbound_pvs" '{
      group_by: $group_by,
      pricing_source: $pricing_source,
      currency: $currency,
      namespace: (if $namespace == "" then null else $namespace end),
      label_selector: (if $label_selector == "" then null else $label_selector end),
      top_n: (if $top_n == 0 then null else $top_n end),
      total_hourly_cost: $total_hourly,
      total_monthly_cost: $total_monthly,
      total_compute_monthly: $total_compute_monthly,
      total_storage_monthly: $total_storage_monthly,
      total_lb_monthly: $total_lb_monthly,
      entries: $entries,
      storage_costs: $storage_costs,
      lb_services: $lb_services,
      unbound_pvs: $unbound_pvs
    }'
}

###############################################################################
# Main Orchestration
###############################################################################

main() {
  # Step 0: Preflight
  preflight

  # Step 1: Collect node data
  local node_data
  node_data=$(get_node_data)

  local node_count
  node_count=$(echo "$node_data" | jq 'length')
  if [ "$node_count" -eq 0 ]; then
    error_json "no nodes found with allocatable resources"
  fi

  # Step 2: Determine pricing mode
  local effective_mode="$PRICING_MODE"
  if [ "$effective_mode" = "auto" ]; then
    effective_mode=$(detect_cloud_provider)
  fi

  local pricing_source="$effective_mode"

  # Step 3: Collect pod data
  local pod_data
  pod_data=$(get_pod_data)

  local pod_count
  pod_count=$(echo "$pod_data" | jq 'length')
  # Step 3.5: Collect storage and LoadBalancer data (best-effort)
  local pvc_data="[]" unbound_pvs="[]" storage_costs="[]"
  local lb_services="[]" lb_costs="[]"

  if pvc_data=$(get_pvc_data 2>/dev/null); then
    storage_costs=$(compute_storage_costs "$pvc_data" 2>/dev/null) || storage_costs="[]"
  else
    pvc_data="[]"; storage_costs="[]"
  fi

  if ! unbound_pvs=$(get_unbound_pvs 2>/dev/null); then
    unbound_pvs="[]"
  fi

  # Annotate unbound PVs with waste cost
  local storage_rate="$DEFAULT_STORAGE_GIB_MONTHLY"
  if [ "$PRICING_MODE" = "custom" ] && [ -n "${CUSTOM_PRICING_FILE:-}" ]; then
    local cr
    cr=$(jq -r '.storage_gib_monthly // empty' "$CUSTOM_PRICING_FILE" 2>/dev/null || true)
    [ -n "$cr" ] && storage_rate="$cr"
  fi
  unbound_pvs=$(echo "$unbound_pvs" | jq --arg rate "$storage_rate" '[
    .[] | . + {
      capacity_gib: (.capacity_bytes / 1073741824),
      monthly_waste: ((.capacity_bytes / 1073741824) * ($rate | tonumber))
    }
  ]' 2>/dev/null) || unbound_pvs="[]"

  if lb_services=$(get_lb_services 2>/dev/null); then
    lb_costs=$(compute_lb_costs "$lb_services" 2>/dev/null) || lb_costs="[]"
  else
    lb_services="[]"; lb_costs="[]"
  fi

  # Verify we have at least some data to report
  local storage_count lb_svc_count unbound_pv_count
  storage_count=$(echo "$storage_costs" | jq 'length')
  lb_svc_count=$(echo "$lb_costs" | jq 'length')
  unbound_pv_count=$(echo "$unbound_pvs" | jq 'length')
  if [ "$pod_count" -eq 0 ] && [ "$storage_count" -eq 0 ] && [ "$lb_svc_count" -eq 0 ] && [ "$unbound_pv_count" -eq 0 ]; then
    error_json "no running pods, PVCs, or LoadBalancer services found"
  fi

  # Step 4: Compute costs (skip if no pods to avoid unnecessary pricing API calls)
  local pod_costs="[]"
  if [ "$pod_count" -gt 0 ]; then
    pod_costs=$(compute_costs "$node_data" "$pod_data" "$effective_mode") || error_json "failed to compute pod costs"
  fi

  # Step 5: Group and aggregate
  local grouped
  grouped=$(group_costs "$pod_costs" "$GROUP_BY" "$storage_costs" "$lb_costs") || error_json "failed to group costs"

  # Apply top N filter
  grouped=$(apply_top_n "$grouped" "$TOP_N")

  # Step 6: Generate output
  case "$OUTPUT_FORMAT" in
    markdown)
      generate_markdown "$grouped" "$GROUP_BY" "$pricing_source" "$storage_costs" "$lb_costs" "$unbound_pvs"
      ;;
    json)
      generate_json_output "$grouped" "$GROUP_BY" "$pricing_source" "$storage_costs" "$lb_costs" "$unbound_pvs"
      ;;
  esac
}

main
