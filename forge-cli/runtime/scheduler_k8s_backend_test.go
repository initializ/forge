package runtime

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/initializ/forge/forge-core/scheduler"
)

type fakeBackendLogger struct{}

func (fakeBackendLogger) Info(string, map[string]any)  {}
func (fakeBackendLogger) Debug(string, map[string]any) {}
func (fakeBackendLogger) Warn(string, map[string]any)  {}
func (fakeBackendLogger) Error(string, map[string]any) {}

func newTestK8sBackend(t *testing.T, cfg K8sBackendConfig) (*KubernetesBackend, *fake.Clientset) {
	t.Helper()
	cs := fake.NewSimpleClientset()
	if cfg.ServiceURL == "" {
		cfg.ServiceURL = "http://agent.default.svc:8080/"
	}
	b := NewKubernetesBackendWithClient(cs, "test-agent", "default", cfg, fakeBackendLogger{})
	return b, cs
}

// TestKubernetesBackend_SyncCreatesCronJobsForDeclared verifies the
// Sync happy path: declared yaml entries materialize as CronJobs with
// the right labels + Forge metadata.
func TestKubernetesBackend_SyncCreatesCronJobsForDeclared(t *testing.T) {
	b, cs := newTestK8sBackend(t, K8sBackendConfig{})
	ctx := context.Background()

	declared := []scheduler.Schedule{
		{ID: "daily-summary", Cron: "0 9 * * *", Task: "Send daily summary", Source: scheduler.SourceYAML, Enabled: true},
		{ID: "hourly-heartbeat", Cron: "@hourly", Task: "Ping the channel", Source: scheduler.SourceYAML, Enabled: true},
	}
	if err := b.Sync(ctx, declared); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	list, err := cs.BatchV1().CronJobs("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list cronjobs: %v", err)
	}
	if got, want := len(list.Items), 2; got != want {
		t.Fatalf("created %d CronJobs, want %d", got, want)
	}
	for _, cj := range list.Items {
		if cj.Labels["forge.agent.id"] != "test-agent" {
			t.Errorf("CronJob %q missing forge.agent.id label", cj.Name)
		}
		if cj.Labels["forge.schedule.source"] != "yaml" {
			t.Errorf("CronJob %q source label = %q, want yaml", cj.Name, cj.Labels["forge.schedule.source"])
		}
		if cj.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
			t.Errorf("CronJob %q concurrency = %q, want Forbid", cj.Name, cj.Spec.ConcurrencyPolicy)
		}
	}
}

// TestKubernetesBackend_SyncIdempotent verifies that re-running Sync
// with the same declared set does not churn CronJobs (no spurious
// Updates). Reconciliation is measured by counting Update actions on
// the fake clientset's action recorder.
func TestKubernetesBackend_SyncIdempotent(t *testing.T) {
	b, cs := newTestK8sBackend(t, K8sBackendConfig{})
	ctx := context.Background()

	declared := []scheduler.Schedule{
		{ID: "s1", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true},
	}
	if err := b.Sync(ctx, declared); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	// Clear actions and re-Sync with no changes.
	cs.ClearActions()
	if err := b.Sync(ctx, declared); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	for _, act := range cs.Actions() {
		if act.GetVerb() == "update" || act.GetVerb() == "patch" {
			t.Errorf("idempotent Sync should not Update; got verb=%s resource=%s", act.GetVerb(), act.GetResource().Resource)
		}
	}
}

// TestKubernetesBackend_SyncUpdatesOnDrift verifies the diff path:
// changing the cron schedule on a declared entry triggers an Update.
func TestKubernetesBackend_SyncUpdatesOnDrift(t *testing.T) {
	b, cs := newTestK8sBackend(t, K8sBackendConfig{})
	ctx := context.Background()

	first := []scheduler.Schedule{{ID: "s1", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true}}
	if err := b.Sync(ctx, first); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	cs.ClearActions()
	second := []scheduler.Schedule{{ID: "s1", Cron: "*/15 * * * *", Task: "t", Source: scheduler.SourceYAML, Enabled: true}}
	if err := b.Sync(ctx, second); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	sawUpdate := false
	for _, act := range cs.Actions() {
		if act.GetVerb() == "update" && act.GetResource().Resource == "cronjobs" {
			sawUpdate = true
			break
		}
	}
	if !sawUpdate {
		t.Errorf("Sync should Update CronJob on cron drift; actions: %v", cs.Actions())
	}
}

// TestKubernetesBackend_SyncPrunesRemovedYAMLEntries verifies
// declarative cleanup: a yaml entry dropped from the manifest
// triggers a CronJob delete on next Sync.
func TestKubernetesBackend_SyncPrunesRemovedYAMLEntries(t *testing.T) {
	b, cs := newTestK8sBackend(t, K8sBackendConfig{})
	ctx := context.Background()

	first := []scheduler.Schedule{
		{ID: "a", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true},
		{ID: "b", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true},
	}
	if err := b.Sync(ctx, first); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	// Drop "b" from the manifest.
	second := []scheduler.Schedule{{ID: "a", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true}}
	if err := b.Sync(ctx, second); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	list, _ := cs.BatchV1().CronJobs("default").List(ctx, metav1.ListOptions{})
	if got := len(list.Items); got != 1 {
		t.Errorf("after pruning Sync: %d CronJobs, want 1", got)
	}
	for _, cj := range list.Items {
		if cj.Labels["forge.schedule.id"] == "b" {
			t.Errorf("pruned entry 'b' still in cluster")
		}
	}
}

// TestKubernetesBackend_SyncPreservesLLMSourced is the safety
// invariant: LLM-set CronJobs (label forge.schedule.source=llm) MUST
// survive a Sync that doesn't list them. The LLM owns them; only the
// dynamic Delete path (gated by AllowDynamic) removes them.
func TestKubernetesBackend_SyncPreservesLLMSourced(t *testing.T) {
	b, cs := newTestK8sBackend(t, K8sBackendConfig{AllowDynamic: true})
	ctx := context.Background()

	// LLM creates a schedule via Set.
	llmSched := scheduler.Schedule{ID: "from-chat", Cron: "@daily", Task: "follow up", Source: scheduler.SourceLLM, Enabled: true}
	if err := b.Set(ctx, llmSched); err != nil {
		t.Fatalf("Set LLM: %v", err)
	}

	// Sync with a non-overlapping yaml entry — must not touch the
	// LLM-sourced CronJob.
	yamlEntry := []scheduler.Schedule{{ID: "yaml-1", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true}}
	if err := b.Sync(ctx, yamlEntry); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	llmName := scheduler.CronJobName("test-agent", "from-chat")
	if _, err := cs.BatchV1().CronJobs("default").Get(ctx, llmName, metav1.GetOptions{}); err != nil {
		t.Errorf("LLM-sourced CronJob was pruned by Sync: %v", err)
	}
}

// TestKubernetesBackend_DynamicSetGatedByAllowDynamic pins the
// AllowDynamic invariant: when off, LLM-source Set returns an error
// referencing the config flag so operators see the path forward.
func TestKubernetesBackend_DynamicSetGatedByAllowDynamic(t *testing.T) {
	b, _ := newTestK8sBackend(t, K8sBackendConfig{AllowDynamic: false})
	ctx := context.Background()

	err := b.Set(ctx, scheduler.Schedule{ID: "llm-1", Cron: "@daily", Task: "x", Source: scheduler.SourceLLM, Enabled: true})
	if err == nil {
		t.Fatal("expected error when AllowDynamic=false")
	}
	if !strings.Contains(err.Error(), "allow_dynamic") {
		t.Errorf("error should reference allow_dynamic config flag; got: %v", err)
	}
}

// TestKubernetesBackend_DynamicDeleteRefusedOnYAMLSource confirms
// the LLM cannot delete an operator-declared schedule via the
// dynamic Delete path. Schedule with forge.schedule.source=yaml is
// always pruned via Sync only.
func TestKubernetesBackend_DynamicDeleteRefusedOnYAMLSource(t *testing.T) {
	b, _ := newTestK8sBackend(t, K8sBackendConfig{AllowDynamic: true})
	ctx := context.Background()

	if err := b.Sync(ctx, []scheduler.Schedule{
		{ID: "yaml-1", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	err := b.Delete(ctx, "yaml-1")
	if err == nil {
		t.Fatal("expected error when deleting yaml-sourced schedule via Delete")
	}
	if !strings.Contains(err.Error(), "forge.yaml") {
		t.Errorf("error should reference forge.yaml; got: %v", err)
	}
}

// TestKubernetesBackend_ListReturnsForgeOwnedOnly verifies List
// filters by the forge.agent.id label so unrelated CronJobs in the
// namespace don't appear in schedule_list output.
func TestKubernetesBackend_ListReturnsForgeOwnedOnly(t *testing.T) {
	b, cs := newTestK8sBackend(t, K8sBackendConfig{})
	ctx := context.Background()

	// Pre-seed an unrelated CronJob (different agent, no Forge labels).
	if _, err := cs.BatchV1().CronJobs("default").Create(ctx, &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unrelated-cronjob",
			Labels: map[string]string{
				"app": "some-other-app",
			},
		},
		Spec: batchv1.CronJobSpec{Schedule: "@daily"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed unrelated cronjob: %v", err)
	}

	if err := b.Sync(ctx, []scheduler.Schedule{
		{ID: "mine", Cron: "@hourly", Task: "t", Source: scheduler.SourceYAML, Enabled: true},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	listed, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := len(listed), 1; got != want {
		t.Fatalf("List returned %d entries, want %d", got, want)
	}
	if listed[0].ID != "mine" {
		t.Errorf("unexpected entry: %+v", listed[0])
	}
}

// TestKubernetesBackend_HistoryIsEmptyWithWarning documents the
// deferred-to-audit-stream behavior. Returns empty slice, no error,
// logs once.
func TestKubernetesBackend_HistoryIsEmptyWithWarning(t *testing.T) {
	b, _ := newTestK8sBackend(t, K8sBackendConfig{})
	hist, err := b.History(context.Background(), "any", 50)
	if err != nil {
		t.Fatalf("History should not error: %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("History should return empty list; got %d", len(hist))
	}
}

// TestKubernetesBackend_ServiceURLDefaultDerivation is the #179 regression
// pin: when scheduler.kubernetes.service_url is unset, the runtime must
// fall back to the same in-cluster Service DNS the build-time
// schedule-manifest stage stamps into generated CronJob YAML. Pre-fix
// the constructor hard-errored when ServiceURL was empty; an operator
// who deployed without an explicit service_url couldn't start the
// agent in-cluster.
func TestKubernetesBackend_ServiceURLDefaultDerivation(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewKubernetesBackendWithClient(cs, "my-agent", "ns-a", K8sBackendConfig{Port: 9090}, fakeBackendLogger{})
	if got, want := b.cfg.ServiceURL, "http://my-agent.ns-a.svc:9090/"; got != want {
		t.Errorf("derived ServiceURL = %q, want %q", got, want)
	}
}

// TestKubernetesBackend_ServiceURLDefaultPortFallback covers the
// port-unset branch: when K8sBackendConfig.Port is zero (e.g. the
// caller didn't plumb r.cfg.Port through), the derivation falls back
// to 8080 to match the runner's listen-port default.
func TestKubernetesBackend_ServiceURLDefaultPortFallback(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewKubernetesBackendWithClient(cs, "my-agent", "ns-a", K8sBackendConfig{}, fakeBackendLogger{})
	if got, want := b.cfg.ServiceURL, "http://my-agent.ns-a.svc:8080/"; got != want {
		t.Errorf("derived ServiceURL with Port=0 = %q, want %q", got, want)
	}
}

// TestKubernetesBackend_ServiceURLExplicitOverride confirms an
// operator-supplied ServiceURL passes through untouched — the
// derivation only applies when the field is empty. Pins the
// non-regression case for operators behind an Ingress / Gateway.
func TestKubernetesBackend_ServiceURLExplicitOverride(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewKubernetesBackendWithClient(cs, "my-agent", "ns-a",
		K8sBackendConfig{ServiceURL: "https://gateway.example.com/agents/my-agent/", Port: 9090},
		fakeBackendLogger{})
	if got, want := b.cfg.ServiceURL, "https://gateway.example.com/agents/my-agent/"; got != want {
		t.Errorf("explicit ServiceURL not preserved: got %q, want %q", got, want)
	}
}
