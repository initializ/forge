package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/scheduler"
)

// ScheduleManifestStage emits Kubernetes CronJob manifests, a
// credential-less Secret template, and a Role/RoleBinding pair for
// every entry in forge.yaml `schedules[]` when the scheduler block
// requests a Kubernetes-aware deploy. Issue #162 part 3.
//
// Output files land in <output>/k8s/:
//
//	cronjob-<schedule-id>.yaml     (one per schedule)
//	internal-token-secret.yaml     (credential-less template)
//	scheduler-role.yaml            (Role with verbs gated by allow_dynamic)
//	scheduler-rolebinding.yaml     (binds the Role to the agent's SA)
//
// The Secret manifest deliberately ships WITHOUT a `data:` field so
// the artifact is safe to commit. Operators populate it out-of-band
// via `forge auth secret-yaml | kubectl apply -f -` (#162 part 1) or
// their preferred secret manager. Applying the Deployment before the
// Secret is populated leaves the agent pod NotReady with a clear
// `secret "..." not found` event — failure is loud, not silent.
//
// This stage runs unconditionally; when forge.yaml has no schedules
// or no scheduler block, Execute is a no-op. When the scheduler
// block requests the file backend explicitly (`backend: file`),
// CronJobs are not emitted — operators using file mode in production
// would be a misconfiguration we don't want to encourage.
type ScheduleManifestStage struct{}

func (s *ScheduleManifestStage) Name() string { return "generate-schedule-manifests" }

func (s *ScheduleManifestStage) Execute(_ context.Context, bc *pipeline.BuildContext) error {
	if bc.Config == nil {
		return nil
	}
	cfg := bc.Config
	if len(cfg.Schedules) == 0 {
		return nil
	}
	// "file" explicitly opts out; "kubernetes" and "auto" (default)
	// both emit manifests so the manifests are usable whether the
	// runtime resolves to file or k8s.
	if cfg.Scheduler.Backend == "file" {
		return nil
	}

	k8sDir := filepath.Join(bc.Opts.OutputDir, "k8s")
	if err := os.MkdirAll(k8sDir, 0755); err != nil {
		return fmt.Errorf("creating k8s directory: %w", err)
	}

	agentID := cfg.AgentID
	if agentID == "" {
		return fmt.Errorf("schedule manifest stage: forge.yaml agent_id is required")
	}
	ns := cfg.Scheduler.Kubernetes.Namespace
	if ns == "" {
		ns = "default"
	}
	serviceURL := cfg.Scheduler.Kubernetes.ServiceURL
	if serviceURL == "" {
		// Best-effort default: in-cluster Service DNS for the agent
		// on the standard A2A port. Operators should set
		// scheduler.kubernetes.service_url explicitly when the agent
		// pod listens on a non-default port or in a non-default
		// namespace (or behind an Ingress / Gateway).
		port := 8080
		if bc.Spec != nil && bc.Spec.Runtime != nil && bc.Spec.Runtime.Port != 0 {
			port = bc.Spec.Runtime.Port
		}
		serviceURL = fmt.Sprintf("http://%s.%s.svc:%d/", agentID, ns, port)
	}
	authSecret := cfg.Scheduler.Kubernetes.AuthSecretName
	if authSecret == "" {
		authSecret = agentID + "-internal-token"
	}

	// One CronJob per declared schedule.
	for _, sc := range cfg.Schedules {
		yaml := scheduler.CronJobYAML(scheduler.CronJobManifestInput{
			AgentID:        agentID,
			Namespace:      ns,
			ServiceURL:     serviceURL,
			AuthSecretName: authSecret,
			TriggerImage:   cfg.Scheduler.Kubernetes.TriggerImage,
			Schedule: scheduler.Schedule{
				ID:            sc.ID,
				Cron:          sc.Cron,
				Task:          sc.Task,
				Skill:         sc.Skill,
				Channel:       sc.Channel,
				ChannelTarget: sc.ChannelTarget,
				Source:        scheduler.SourceYAML,
				Enabled:       true,
			},
		})
		name := "cronjob-" + safeFileName(sc.ID) + ".yaml"
		path := filepath.Join(k8sDir, name)
		if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
		bc.AddFile(filepath.Join("k8s", name), path)
	}

	// Credential-less Secret template (operator populates out-of-band).
	secretYAML := buildInternalTokenSecretTemplate(authSecret, ns, agentID)
	secretPath := filepath.Join(k8sDir, "internal-token-secret.yaml")
	if err := os.WriteFile(secretPath, []byte(secretYAML), 0644); err != nil {
		return fmt.Errorf("writing internal-token-secret.yaml: %w", err)
	}
	bc.AddFile(filepath.Join("k8s", "internal-token-secret.yaml"), secretPath)

	// RBAC: Role with verbs gated by allow_dynamic, plus the binding.
	allowDynamic := cfg.Scheduler.Kubernetes.AllowDynamic
	rolePath := filepath.Join(k8sDir, "scheduler-role.yaml")
	if err := os.WriteFile(rolePath, []byte(buildSchedulerRole(agentID, ns, allowDynamic)), 0644); err != nil {
		return fmt.Errorf("writing scheduler-role.yaml: %w", err)
	}
	bc.AddFile(filepath.Join("k8s", "scheduler-role.yaml"), rolePath)

	rbPath := filepath.Join(k8sDir, "scheduler-rolebinding.yaml")
	if err := os.WriteFile(rbPath, []byte(buildSchedulerRoleBinding(agentID, ns)), 0644); err != nil {
		return fmt.Errorf("writing scheduler-rolebinding.yaml: %w", err)
	}
	bc.AddFile(filepath.Join("k8s", "scheduler-rolebinding.yaml"), rbPath)

	return nil
}

// buildInternalTokenSecretTemplate returns a Secret manifest WITHOUT
// the `data:` field. The commented block documents the three out-of-
// band populate paths so an operator inspecting the file knows how to
// fill it in without reading the docs separately.
func buildInternalTokenSecretTemplate(name, namespace, agentID string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    forge.agent.id: %s
type: Opaque
# data:
#   token: <BASE64-OF-RUNTIME-TOKEN>
#
# This Secret is intentionally generated WITHOUT a `+"`data`"+` field.
# The token is a security credential and must NOT be checked into
# version control. Populate it once per deployment via one of:
#
#   1. forge auth secret-yaml | kubectl apply -f -
#      (one-liner using the local <agent-root>/.forge/runtime.token)
#
#   2. ExternalSecrets / Sealed Secrets / SOPS / Vault Agent Injector
#      with a manifest pointing at this Secret name.
#
#   3. From a clean-checkout first deploy:
#        forge auth mint-token > /dev/null
#        forge auth secret-yaml | kubectl apply -f -
#
# The Deployment references this Secret by name; applying it before
# the Secret is populated leaves the pod NotReady with a clear
# 'secret "%s" not found' event.
`, name, namespace, agentID, name)
}

// buildSchedulerRole returns the Role manifest scoped to the agent's
// namespace. Verbs are split: get/list always present (powers the
// schedule_list builtin tool), create/update/delete gated by
// allow_dynamic to keep operators from inadvertently granting the
// LLM CronJob CRUD authority.
func buildSchedulerRole(agentID, namespace string, allowDynamic bool) string {
	verbs := `["get", "list", "watch"]`
	if allowDynamic {
		verbs = `["get", "list", "watch", "create", "update", "patch", "delete"]`
	}
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %s-scheduler
  namespace: %s
  labels:
    forge.agent.id: %s
# Allow the agent pod to read its own CronJobs (powers the
# schedule_list / schedule_history tools). create/update/delete verbs
# are present only when scheduler.kubernetes.allow_dynamic=true, which
# lets the LLM-driven schedule_set / schedule_delete builtin tools
# materialize CronJobs at runtime. Grant carefully.
rules:
  - apiGroups: ["batch"]
    resources: ["cronjobs"]
    verbs: %s
`, agentID, namespace, agentID, verbs)
}

// buildSchedulerRoleBinding binds the scheduler-role to the agent's
// ServiceAccount. Assumes the Deployment uses a ServiceAccount named
// after the agent_id (matches the convention forge package's
// deployment template established).
func buildSchedulerRoleBinding(agentID, namespace string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %s-scheduler
  namespace: %s
  labels:
    forge.agent.id: %s
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %s-scheduler
`, agentID, namespace, agentID, agentID, namespace, agentID)
}

// safeFileName replaces characters that are valid in schedule IDs
// (per the cron parser) but unfriendly in shell-completed paths.
// Lowercase and substitute everything outside the K8s name set with
// '-' — matches what scheduler.CronJobName does for the K8s resource
// name, but applied to the on-disk filename so `ls k8s/` is readable.
func safeFileName(id string) string {
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range strings.ToLower(id) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}
