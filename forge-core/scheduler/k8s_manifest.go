package scheduler

import (
	"crypto/sha1" //nolint:gosec // hash is for name derivation, not security
	"encoding/hex"
	"fmt"
	"strings"
)

// CronJobManifestInput is the data the manifest builders consume. The
// runtime KubernetesBackend (this file) and the upcoming `forge package`
// stage (#162 part 3) both feed it; the manifest text + the in-memory
// CronJob spec must agree byte-for-byte so the runtime's Sync
// reconciliation doesn't churn against manifest-applied resources.
type CronJobManifestInput struct {
	// AgentID is the operator's agent identifier, used as a label and
	// as the prefix of the CronJob's name.
	AgentID string
	// Namespace is the target K8s namespace. Empty defaults to the
	// agent pod's own namespace (resolved at runtime from the pod's
	// downward API or the in-cluster config).
	Namespace string
	// ServiceURL is the in-cluster URL CronJob curl requests target.
	// Typically `http://<agent-svc>.<ns>.svc:<port>/`.
	ServiceURL string
	// AuthSecretName is the K8s Secret holding the internal token the
	// CronJob mounts and sends as Bearer auth. Defaults to
	// `<agent-id>-internal-token` matching `forge auth secret-yaml`.
	AuthSecretName string
	// TriggerImage is the container image the CronJob runs to make
	// the curl request. Defaults to `curlimages/curl:8.10.1`.
	TriggerImage string
	// Schedule is the Forge schedule entry to materialize.
	Schedule Schedule
}

// DefaultTriggerImage is the curl image the CronJob's trigger
// container runs by default. Pinned to a specific tag so a registry
// pull is reproducible.
const DefaultTriggerImage = "curlimages/curl:8.10.1"

// CronJobName returns the deterministic K8s resource name for a
// schedule. K8s resource names are constrained to 63 chars with a
// restricted character set; we hash-suffix when the natural name
// would exceed the limit to keep the name unique.
func CronJobName(agentID, scheduleID string) string {
	base := fmt.Sprintf("forge-%s-%s", agentID, scheduleID)
	base = sanitizeK8sName(base)
	if len(base) <= 63 {
		return base
	}
	// Truncate + suffix with a hash so distinct (agent_id, sched_id)
	// pairs that share a prefix don't collide after truncation.
	h := sha1.Sum([]byte(fmt.Sprintf("%s/%s", agentID, scheduleID))) //nolint:gosec
	suffix := "-" + hex.EncodeToString(h[:4])
	return base[:63-len(suffix)] + suffix
}

// sanitizeK8sName replaces characters that aren't valid in a K8s
// resource name (RFC 1123 subset: lowercase alphanumeric + '-') with
// '-'. The output is forced lowercase. Empty input becomes "forge".
func sanitizeK8sName(s string) string {
	if s == "" {
		return "forge"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	out := b.String()
	// Trim leading/trailing '-' which K8s rejects.
	out = strings.Trim(out, "-")
	if out == "" {
		return "forge"
	}
	return out
}

// CronJobYAML returns the apiVersion=batch/v1 CronJob manifest text
// for a Schedule. Reused by the KubernetesBackend (formatted with
// Sprintf and passed through yaml.Unmarshal → batchv1.CronJob for
// the Create/Patch API call) and by the `forge package` build stage
// in part 3 (written verbatim to the k8s/ directory for
// `kubectl apply -k`).
//
// The trigger container's args are an A2A JSON-RPC tasks/send body
// that includes the schedule task as the user message. The cluster
// substitutes $(...) shell expansions via the curl image's shell;
// the $(date +%s) generates a unique task ID per fire.
//
// All values are inlined (no Helm-template placeholders) so the
// manifest is operator-readable and `kubectl diff`-able against the
// running state.
func CronJobYAML(input CronJobManifestInput) string {
	image := input.TriggerImage
	if image == "" {
		image = DefaultTriggerImage
	}
	authSecret := input.AuthSecretName
	if authSecret == "" {
		authSecret = input.AgentID + "-internal-token"
	}
	namespace := input.Namespace
	if namespace == "" {
		namespace = "default"
	}
	name := CronJobName(input.AgentID, input.Schedule.ID)

	taskText := escapeForSingleQuotedJSON(input.Schedule.Task)

	return fmt.Sprintf(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: %s
  namespace: %s
  labels:
    forge.agent.id: %s
    forge.schedule.id: %s
    forge.schedule.source: %s
spec:
  schedule: %q
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: trigger
              image: %s
              env:
                - name: FORGE_AUTH_TOKEN
                  valueFrom:
                    secretKeyRef:
                      name: %s
                      key: token
              command: ["sh", "-c"]
              args:
                - |
                  curl -sfX POST %s \
                    -H "Authorization: Bearer $FORGE_AUTH_TOKEN" \
                    -H "X-Forge-Schedule-Id: %s" \
                    -H "Content-Type: application/json" \
                    --data '{"jsonrpc":"2.0","id":"1","method":"tasks/send","params":{"id":"sched-%s-'$(date +%%s)'","message":{"role":"user","parts":[{"type":"text","text":"%s"}]}}}'
`,
		name, namespace,
		input.AgentID, input.Schedule.ID, defaultSource(input.Schedule.Source),
		input.Schedule.Cron,
		image,
		authSecret,
		input.ServiceURL,
		input.Schedule.ID,
		input.Schedule.ID,
		taskText,
	)
}

// defaultSource returns "yaml" when the schedule has no source set;
// matches the existing scheduler convention for declarative entries.
func defaultSource(s string) string {
	if s == "" {
		return SourceYAML
	}
	return s
}

// escapeForSingleQuotedJSON escapes characters that would break the
// JSON body wrapped in single quotes inside the shell `args:` line:
// single quotes (close + reopen) and double quotes (close the JSON
// string). Newlines collapse to spaces. Not a full JSON encoder —
// the task text comes from forge.yaml or the LLM and is expected to
// be human-readable prose. Operators who need richer escaping can
// override the trigger image.
func escapeForSingleQuotedJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "'", `'"'"'`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}
