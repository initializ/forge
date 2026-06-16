package runtime

import (
	"context"
	"fmt"
	"os"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/scheduler"
)

// Label keys + annotation keys the KubernetesBackend stamps on every
// CronJob it owns. Operators grep the cluster by these:
//
//	kubectl get cronjobs -l forge.agent.id=<agent>
//	kubectl describe cronjob -l forge.schedule.source=llm
//
// The annotations carry Forge-specific fields that don't fit the
// CronJob spec (the natural-language task text, the optional skill,
// and the optional channel target). They round-trip 1:1 with the
// scheduler.Schedule struct.
const (
	labelAgentID        = "forge.agent.id"
	labelScheduleID     = "forge.schedule.id"
	labelScheduleSource = "forge.schedule.source"

	annotationTask          = "forge.schedule.task"
	annotationSkill         = "forge.schedule.skill"
	annotationChannel       = "forge.schedule.channel"
	annotationChannelTarget = "forge.schedule.channel_target"
	annotationRunCount      = "forge.schedule.run_count"
	annotationLastStatus    = "forge.schedule.last_status"
)

// KubernetesBackend implements scheduler.Backend by delegating
// persistence + timing to the cluster's CronJob controller. See
// docs/deployment/scheduler-kubernetes.md and issue #162.
type KubernetesBackend struct {
	client    kubernetes.Interface
	namespace string
	agentID   string
	cfg       K8sBackendConfig
	logger    coreruntime.Logger

	// warnedHistory keeps the schedule_history fallback warning from
	// spamming the ops log — emit once per backend instance.
	warnedHistory bool
}

// K8sBackendConfig carries the runtime tuning the KubernetesBackend
// needs above and beyond the CronJob manifest defaults. Sourced from
// forge.yaml's `scheduler.kubernetes` block + the resolved agent_id +
// in-cluster service URL.
type K8sBackendConfig struct {
	// ServiceURL is the in-cluster URL CronJob trigger pods POST to.
	// When empty, the constructor derives the standard in-cluster
	// Service DNS: http://<agent_id>.<namespace>.svc:<port>/ . This
	// matches the value the build-time schedule-manifest stage stamps
	// into generated CronJob YAML (see forge-cli/build/schedule_manifest_stage.go).
	// Operators set this explicitly when the agent listens on a
	// non-standard port or sits behind an Ingress / Gateway.
	ServiceURL string
	// Port is the port the agent's A2A server listens on; combined
	// with agent_id and namespace to derive ServiceURL when unset.
	// Defaults to 8080 when zero (matches the runner's listen-port
	// default in forge-cli/runtime/runner.go).
	Port int
	// AuthSecretName is the K8s Secret containing the internal bearer
	// token CronJobs mount. Defaults to "<agent_id>-internal-token"
	// when empty (matches `forge auth secret-yaml`).
	AuthSecretName string
	// TriggerImage is the container image the CronJob's trigger pod
	// runs. Defaults to scheduler.DefaultTriggerImage when empty.
	TriggerImage string
	// AllowDynamic gates whether Set / Delete calls (from the LLM
	// `schedule_set` / `schedule_delete` builtin tools) can create or
	// remove CronJobs at runtime. Default false — Set returns a
	// clear error explaining the rationale. Sync (declarative) is
	// always allowed regardless of this flag.
	AllowDynamic bool
}

// defaultK8sServiceURL returns the in-cluster Service DNS URL the
// runtime falls back to when ServiceURL is unset. Mirrors the
// build-time default in forge-cli/build/schedule_manifest_stage.go so
// a `forge run` inside a pod without an explicit service_url still
// dispatches CronJob triggers at the same address `forge package`
// would have stamped into the generated CronJob YAML. Port defaults
// to 8080 when port <= 0.
func defaultK8sServiceURL(agentID, namespace string, port int) string {
	if port <= 0 {
		port = 8080
	}
	return fmt.Sprintf("http://%s.%s.svc:%d/", agentID, namespace, port)
}

// NewKubernetesBackend builds a backend wired to an in-cluster
// kubernetes.Interface. The namespace defaults to the pod's own
// namespace, read from the standard projected
// /var/run/secrets/kubernetes.io/serviceaccount/namespace file when
// the caller passes "". Returns a typed error when not running
// in-cluster (the in-cluster config probe fails) — the runner
// surfaces this as a startup abort when `scheduler.backend:
// kubernetes` was explicitly requested.
func NewKubernetesBackend(agentID, namespace string, cfg K8sBackendConfig, logger coreruntime.Logger) (*KubernetesBackend, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes scheduler backend: in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes scheduler backend: client: %w", err)
	}
	if namespace == "" {
		namespace = readPodNamespace()
	}
	if namespace == "" {
		namespace = "default"
	}
	if cfg.ServiceURL == "" {
		cfg.ServiceURL = defaultK8sServiceURL(agentID, namespace, cfg.Port)
	}
	if cfg.AuthSecretName == "" {
		cfg.AuthSecretName = agentID + "-internal-token"
	}
	if cfg.TriggerImage == "" {
		cfg.TriggerImage = scheduler.DefaultTriggerImage
	}
	return &KubernetesBackend{
		client:    cs,
		namespace: namespace,
		agentID:   agentID,
		cfg:       cfg,
		logger:    logger,
	}, nil
}

// NewKubernetesBackendWithClient is the testing seam: callers (unit
// tests against `fake.Clientset`) pass an explicit kubernetes.Interface
// instead of probing the in-cluster config. Production code uses
// NewKubernetesBackend.
func NewKubernetesBackendWithClient(client kubernetes.Interface, agentID, namespace string, cfg K8sBackendConfig, logger coreruntime.Logger) *KubernetesBackend {
	if namespace == "" {
		namespace = "default"
	}
	if cfg.ServiceURL == "" {
		cfg.ServiceURL = defaultK8sServiceURL(agentID, namespace, cfg.Port)
	}
	if cfg.AuthSecretName == "" {
		cfg.AuthSecretName = agentID + "-internal-token"
	}
	if cfg.TriggerImage == "" {
		cfg.TriggerImage = scheduler.DefaultTriggerImage
	}
	return &KubernetesBackend{
		client:    client,
		namespace: namespace,
		agentID:   agentID,
		cfg:       cfg,
		logger:    logger,
	}
}

// Start is a no-op — the cluster's CronJob controller owns timing.
// No goroutines, no ticker.
func (b *KubernetesBackend) Start(_ context.Context) {}

// Stop is a no-op for the same reason.
func (b *KubernetesBackend) Stop() {}

// Reload is a no-op — every Backend method hits the API directly,
// no cached state to refresh.
func (b *KubernetesBackend) Reload(_ context.Context) {}

// Sync reconciles cluster CronJobs against the declared yaml entries.
//
//   - For each declared entry: create the CronJob if absent, patch
//     when the spec drifted, leave alone when in sync.
//   - For each EXISTING yaml-sourced CronJob NOT in declared: delete.
//   - LLM-sourced CronJobs (label forge.schedule.source=llm) are
//     left alone regardless of the declared list — the LLM owns them
//     via Set / Delete.
//
// Mirrors the FileBackend.Sync rule from forge-core/scheduler/backend.go.
func (b *KubernetesBackend) Sync(ctx context.Context, declared []scheduler.Schedule) error {
	declaredByID := make(map[string]scheduler.Schedule, len(declared))
	for _, s := range declared {
		if s.Source == "" {
			s.Source = scheduler.SourceYAML
		}
		declaredByID[s.ID] = s
	}

	existing, err := b.client.BatchV1().CronJobs(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", labelAgentID, b.agentID),
	})
	if err != nil {
		return fmt.Errorf("list cronjobs: %w", err)
	}
	existingByID := make(map[string]batchv1.CronJob, len(existing.Items))
	for _, cj := range existing.Items {
		id := cj.Labels[labelScheduleID]
		if id != "" {
			existingByID[id] = cj
		}
	}

	// Upsert declared entries.
	for _, s := range declared {
		want := b.cronJobFromSchedule(s)
		if cur, ok := existingByID[s.ID]; ok {
			if !cronJobNeedsUpdate(cur, want) {
				continue
			}
			want.ResourceVersion = cur.ResourceVersion
			if _, err := b.client.BatchV1().CronJobs(b.namespace).Update(ctx, want, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update cronjob %s: %w", want.Name, err)
			}
			continue
		}
		if _, err := b.client.BatchV1().CronJobs(b.namespace).Create(ctx, want, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create cronjob %s: %w", want.Name, err)
		}
	}

	// Prune yaml-sourced CronJobs no longer in the declared set.
	for id, cj := range existingByID {
		if _, stillDeclared := declaredByID[id]; stillDeclared {
			continue
		}
		if cj.Labels[labelScheduleSource] != scheduler.SourceYAML {
			continue // LLM-sourced; not ours to prune.
		}
		if err := b.client.BatchV1().CronJobs(b.namespace).Delete(ctx, cj.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale cronjob %s: %w", cj.Name, err)
		}
	}
	return nil
}

// List returns every Forge-owned CronJob in the namespace.
func (b *KubernetesBackend) List(ctx context.Context) ([]scheduler.Schedule, error) {
	cjList, err := b.client.BatchV1().CronJobs(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", labelAgentID, b.agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("list cronjobs: %w", err)
	}
	out := make([]scheduler.Schedule, 0, len(cjList.Items))
	for i := range cjList.Items {
		s, ok := scheduleFromCronJob(&cjList.Items[i])
		if !ok {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// Get returns a single schedule by ID. Returns (nil, nil) when the
// matching CronJob is absent — same contract as ScheduleStore.Get.
func (b *KubernetesBackend) Get(ctx context.Context, id string) (*scheduler.Schedule, error) {
	cj, err := b.client.BatchV1().CronJobs(b.namespace).Get(ctx, scheduler.CronJobName(b.agentID, id), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get cronjob: %w", err)
	}
	s, ok := scheduleFromCronJob(cj)
	if !ok {
		return nil, nil
	}
	return &s, nil
}

// Set creates or updates a single schedule. Gated by AllowDynamic
// when the source is not "yaml": the LLM-driven schedule_set tool
// reaches this path via the schedule_set builtin tool the runner
// registers. Returns a clear error when dynamic creation is disabled.
//
// Declarative sources (Sync with source=yaml) bypass AllowDynamic.
func (b *KubernetesBackend) Set(ctx context.Context, s scheduler.Schedule) error {
	if s.Source == "" {
		s.Source = scheduler.SourceLLM
	}
	if s.Source == scheduler.SourceLLM && !b.cfg.AllowDynamic {
		return fmt.Errorf("dynamic schedule creation is disabled (scheduler.kubernetes.allow_dynamic=false); declare the schedule in forge.yaml or enable allow_dynamic")
	}
	want := b.cronJobFromSchedule(s)
	cur, err := b.client.BatchV1().CronJobs(b.namespace).Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get cronjob: %w", err)
	}
	if apierrors.IsNotFound(err) {
		_, err := b.client.BatchV1().CronJobs(b.namespace).Create(ctx, want, metav1.CreateOptions{})
		return err
	}
	want.ResourceVersion = cur.ResourceVersion
	_, err = b.client.BatchV1().CronJobs(b.namespace).Update(ctx, want, metav1.UpdateOptions{})
	return err
}

// Delete removes a schedule by ID. Gated by AllowDynamic when the
// target CronJob is LLM-sourced; declarative (yaml-sourced) CronJobs
// are only removed by Sync's reconciliation path, not by direct
// Delete calls.
func (b *KubernetesBackend) Delete(ctx context.Context, id string) error {
	name := scheduler.CronJobName(b.agentID, id)
	cur, err := b.client.BatchV1().CronJobs(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get cronjob: %w", err)
	}
	if cur.Labels[labelScheduleSource] == scheduler.SourceYAML {
		return fmt.Errorf("schedule %q is declared in forge.yaml; remove it from the manifest or use Sync to reconcile", id)
	}
	if !b.cfg.AllowDynamic {
		return fmt.Errorf("dynamic schedule deletion is disabled (scheduler.kubernetes.allow_dynamic=false)")
	}
	err = b.client.BatchV1().CronJobs(b.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete cronjob: %w", err)
	}
	return nil
}

// History returns an empty list with a one-time warning. K8s CronJob
// status carries LastScheduleTime + a small Job-history window, but
// the canonical source of truth in K8s mode is the audit stream
// (`schedule_fire` / `schedule_complete` events). The schedule_history
// builtin tool reads from there.
func (b *KubernetesBackend) History(_ context.Context, _ string, _ int) ([]scheduler.HistoryEntry, error) {
	if !b.warnedHistory && b.logger != nil {
		b.logger.Warn("scheduler history is not stored on the kubernetes backend; read from the audit stream (schedule_fire / schedule_complete events)", nil)
		b.warnedHistory = true
	}
	return nil, nil
}

// cronJobFromSchedule builds the in-memory CronJob the K8s API
// expects. The PodSpec body must agree with scheduler.CronJobYAML so
// the runtime reconcile doesn't churn against `forge package`-applied
// manifests.
func (b *KubernetesBackend) cronJobFromSchedule(s scheduler.Schedule) *batchv1.CronJob {
	name := scheduler.CronJobName(b.agentID, s.ID)
	source := s.Source
	if source == "" {
		source = scheduler.SourceYAML
	}
	enabled := s.Enabled
	if !enabled && s.Cron != "" {
		// Treat zero-Enabled as "ON unless caller set Suspend explicitly"
		// — matches the file backend's interpretation where Enabled is
		// the standard runtime tag.
		enabled = true
	}
	suspend := !enabled
	concurrencyForbid := batchv1.ForbidConcurrent
	successHistory := int32(3)
	failHistory := int32(3)
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
			Labels: map[string]string{
				labelAgentID:        b.agentID,
				labelScheduleID:     s.ID,
				labelScheduleSource: source,
			},
			Annotations: map[string]string{
				annotationTask: s.Task,
			},
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   s.Cron,
			ConcurrencyPolicy:          concurrencyForbid,
			SuccessfulJobsHistoryLimit: &successHistory,
			FailedJobsHistoryLimit:     &failHistory,
			Suspend:                    &suspend,
			JobTemplate:                batchv1.JobTemplateSpec{Spec: b.jobSpec(s)},
		},
	}
	if s.Skill != "" {
		cj.Annotations[annotationSkill] = s.Skill
	}
	if s.Channel != "" {
		cj.Annotations[annotationChannel] = s.Channel
	}
	if s.ChannelTarget != "" {
		cj.Annotations[annotationChannelTarget] = s.ChannelTarget
	}
	if s.RunCount > 0 {
		cj.Annotations[annotationRunCount] = strconv.Itoa(s.RunCount)
	}
	if s.LastStatus != "" {
		cj.Annotations[annotationLastStatus] = s.LastStatus
	}
	return cj
}

// jobSpec returns the trigger Job spec — the curl container that hits
// the agent's A2A endpoint when the cron fires. The shape mirrors
// scheduler.CronJobYAML exactly so the runtime and the build-time
// manifest don't drift.
func (b *KubernetesBackend) jobSpec(s scheduler.Schedule) batchv1.JobSpec {
	restartNever := corev1.RestartPolicyNever
	curlArgs := fmt.Sprintf(
		`curl -sfX POST %s -H "Authorization: Bearer $FORGE_AUTH_TOKEN" -H "X-Forge-Schedule-Id: %s" -H "Content-Type: application/json" --data '{"jsonrpc":"2.0","id":"1","method":"tasks/send","params":{"id":"sched-%s-'"$(date +%%s)"'","message":{"role":"user","parts":[{"type":"text","text":"%s"}]}}}'`,
		b.cfg.ServiceURL, s.ID, s.ID, shellEscapeSingleQuoted(s.Task),
	)
	return batchv1.JobSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: restartNever,
				Containers: []corev1.Container{
					{
						Name:    "trigger",
						Image:   b.cfg.TriggerImage,
						Command: []string{"sh", "-c"},
						Args:    []string{curlArgs},
						Env: []corev1.EnvVar{
							{
								Name: "FORGE_AUTH_TOKEN",
								ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: b.cfg.AuthSecretName},
										Key:                  "token",
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// scheduleFromCronJob materializes a scheduler.Schedule from the
// CronJob's labels + annotations. Returns (Schedule{}, false) when
// the CronJob does not carry a forge.schedule.id label.
func scheduleFromCronJob(cj *batchv1.CronJob) (scheduler.Schedule, bool) {
	id := cj.Labels[labelScheduleID]
	if id == "" {
		return scheduler.Schedule{}, false
	}
	s := scheduler.Schedule{
		ID:            id,
		Cron:          cj.Spec.Schedule,
		Task:          cj.Annotations[annotationTask],
		Skill:         cj.Annotations[annotationSkill],
		Channel:       cj.Annotations[annotationChannel],
		ChannelTarget: cj.Annotations[annotationChannelTarget],
		Source:        cj.Labels[labelScheduleSource],
		Enabled:       cj.Spec.Suspend == nil || !*cj.Spec.Suspend,
		Created:       cj.CreationTimestamp.Time,
		LastStatus:    cj.Annotations[annotationLastStatus],
	}
	if v := cj.Annotations[annotationRunCount]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.RunCount = n
		}
	}
	if cj.Status.LastScheduleTime != nil {
		s.LastRun = cj.Status.LastScheduleTime.Time
	}
	return s, true
}

// cronJobNeedsUpdate compares the spec-relevant fields of two CronJob
// objects. Returns true when the runtime should issue an Update.
// Status fields and resource versions are ignored — those are the
// API server's domain.
func cronJobNeedsUpdate(cur batchv1.CronJob, want *batchv1.CronJob) bool {
	if cur.Spec.Schedule != want.Spec.Schedule {
		return true
	}
	if (cur.Spec.Suspend == nil) != (want.Spec.Suspend == nil) {
		return true
	}
	if cur.Spec.Suspend != nil && want.Spec.Suspend != nil && *cur.Spec.Suspend != *want.Spec.Suspend {
		return true
	}
	if cur.Annotations[annotationTask] != want.Annotations[annotationTask] {
		return true
	}
	if cur.Annotations[annotationSkill] != want.Annotations[annotationSkill] {
		return true
	}
	if cur.Annotations[annotationChannel] != want.Annotations[annotationChannel] {
		return true
	}
	if cur.Annotations[annotationChannelTarget] != want.Annotations[annotationChannelTarget] {
		return true
	}
	// Source label is fixed at creation, but compare anyway as a safety net.
	if cur.Labels[labelScheduleSource] != want.Labels[labelScheduleSource] {
		return true
	}
	return false
}

// readPodNamespace returns the namespace from the projected file
// kubelet mounts. Empty when not in-cluster.
func readPodNamespace() string {
	const path = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// shellEscapeSingleQuoted escapes the characters that would break the
// curl --data '...' single-quoted JSON body. Mirrors the helper in
// forge-core/scheduler/k8s_manifest.go so the two manifest paths
// agree byte-for-byte. Newlines collapse to spaces.
func shellEscapeSingleQuoted(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		case '\'':
			out = append(out, '\'', '"', '\'', '"', '\'')
		case '\n', '\r':
			out = append(out, ' ')
		default:
			out = append(out, []byte(string(r))...)
		}
	}
	return string(out)
}
