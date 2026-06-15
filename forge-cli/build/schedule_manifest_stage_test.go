package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
)

// newScheduleStageBC builds a BuildContext with the minimum surface
// area the ScheduleManifestStage reads: a Config with Schedules,
// optional Scheduler block, and a Spec with Runtime.Port for the
// service-URL default.
func newScheduleStageBC(t *testing.T, cfg *types.ForgeConfig) *pipeline.BuildContext {
	t.Helper()
	out := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: out})
	bc.Config = cfg
	bc.Spec = &agentspec.AgentSpec{
		AgentID: cfg.AgentID,
		Runtime: &agentspec.RuntimeConfig{Port: 8080},
	}
	return bc
}

// TestScheduleManifestStage_NoOpWhenNoSchedules verifies the stage is
// a no-op when forge.yaml has no schedules[] block. No k8s/ files for
// scheduler should appear in the output.
func TestScheduleManifestStage_NoOpWhenNoSchedules(t *testing.T) {
	bc := newScheduleStageBC(t, &types.ForgeConfig{AgentID: "agent"})
	if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(bc.Opts.OutputDir, "k8s"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cronjob-") {
			t.Errorf("unexpected cronjob file emitted: %s", e.Name())
		}
	}
}

// TestScheduleManifestStage_NoOpWhenBackendFile verifies that
// explicitly setting scheduler.backend=file opts out of CronJob
// emission. Operators forcing file mode in a packaged deploy
// shouldn't get manifests that conflict with their runtime choice.
func TestScheduleManifestStage_NoOpWhenBackendFile(t *testing.T) {
	bc := newScheduleStageBC(t, &types.ForgeConfig{
		AgentID:   "agent",
		Schedules: []types.ScheduleConfig{{ID: "s1", Cron: "@hourly", Task: "t"}},
		Scheduler: types.SchedulerConfig{Backend: "file"},
	})
	if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, name := range []string{"cronjob-s1.yaml", "internal-token-secret.yaml", "scheduler-role.yaml"} {
		path := filepath.Join(bc.Opts.OutputDir, "k8s", name)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("backend=file should not emit %s; file exists", name)
		}
	}
}

// TestScheduleManifestStage_EmitsCronJobPerSchedule verifies the
// happy path: each forge.yaml schedule gets its own CronJob manifest
// with the right labels, the Secret template lands credential-less,
// and the Role/RoleBinding are scoped to the agent's namespace.
func TestScheduleManifestStage_EmitsCronJobPerSchedule(t *testing.T) {
	bc := newScheduleStageBC(t, &types.ForgeConfig{
		AgentID: "aibuilderdemo",
		Schedules: []types.ScheduleConfig{
			{ID: "daily", Cron: "0 9 * * *", Task: "Send daily summary"},
			{ID: "hourly", Cron: "@hourly", Task: "Ping channel"},
		},
	})
	if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, id := range []string{"daily", "hourly"} {
		path := filepath.Join(bc.Opts.OutputDir, "k8s", "cronjob-"+id+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cronjob-%s.yaml not emitted: %v", id, err)
		}
		body := string(data)
		if !strings.Contains(body, "kind: CronJob") {
			t.Errorf("cronjob-%s.yaml not a CronJob:\n%s", id, body)
		}
		if !strings.Contains(body, "forge.agent.id: aibuilderdemo") {
			t.Errorf("cronjob-%s.yaml missing forge.agent.id label", id)
		}
		if !strings.Contains(body, "forge.schedule.id: "+id) {
			t.Errorf("cronjob-%s.yaml missing schedule.id label", id)
		}
		if !strings.Contains(body, "concurrencyPolicy: Forbid") {
			t.Errorf("cronjob-%s.yaml missing concurrencyPolicy", id)
		}
	}

	// Default service URL when scheduler.kubernetes.service_url unset.
	cronjobDaily, _ := os.ReadFile(filepath.Join(bc.Opts.OutputDir, "k8s", "cronjob-daily.yaml"))
	if !strings.Contains(string(cronjobDaily), "http://aibuilderdemo.default.svc:8080/") {
		t.Errorf("default service URL missing from cronjob-daily.yaml:\n%s", cronjobDaily)
	}
}

// TestScheduleManifestStage_SecretTemplateHasNoDataField is the
// security-critical invariant: the generated Secret manifest MUST NOT
// carry a populated `data:` field. The artifact ends up in version
// control and container images; embedding a credential there would
// be a leak even if the value is "placeholder."
func TestScheduleManifestStage_SecretTemplateHasNoDataField(t *testing.T) {
	bc := newScheduleStageBC(t, &types.ForgeConfig{
		AgentID:   "agent",
		Schedules: []types.ScheduleConfig{{ID: "x", Cron: "@hourly", Task: "t"}},
	})
	if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(bc.Opts.OutputDir, "k8s", "internal-token-secret.yaml"))
	if err != nil {
		t.Fatalf("read secret template: %v", err)
	}
	body := string(data)
	// The literal "data:" line MUST be commented or absent. A
	// non-commented top-level data field would be a token leak path
	// even if its value were a placeholder.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data:") {
			t.Errorf("Secret template contains uncommented data: line — DO NOT EMBED CREDENTIALS\n%s", body)
		}
	}
	if !strings.Contains(body, "type: Opaque") {
		t.Errorf("Secret template missing type: Opaque\n%s", body)
	}
	if !strings.Contains(body, "forge auth secret-yaml") {
		t.Errorf("Secret template should document the forge auth secret-yaml populate path\n%s", body)
	}
}

// TestScheduleManifestStage_RoleVerbsGatedByAllowDynamic pins the
// privilege-gating invariant: with allow_dynamic=false (the
// production default), the Role grants get/list/watch only. Flipping
// allow_dynamic=true grants create/update/delete/patch on top.
func TestScheduleManifestStage_RoleVerbsGatedByAllowDynamic(t *testing.T) {
	// verbsLine extracts the `verbs: [...]` line from the Role
	// manifest. We assert against ONLY this line (not the full
	// document) because the surrounding prose comment legitimately
	// references "create/update/delete" as documentation — checking
	// the whole document for those tokens would catch the comment as
	// a false positive.
	verbsLine := func(body string) string {
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "verbs:") {
				return trimmed
			}
		}
		return ""
	}

	t.Run("default disallows write verbs", func(t *testing.T) {
		bc := newScheduleStageBC(t, &types.ForgeConfig{
			AgentID:   "agent",
			Schedules: []types.ScheduleConfig{{ID: "x", Cron: "@hourly", Task: "t"}},
		})
		if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		data, _ := os.ReadFile(filepath.Join(bc.Opts.OutputDir, "k8s", "scheduler-role.yaml"))
		line := verbsLine(string(data))
		if line != `verbs: ["get", "list", "watch"]` {
			t.Errorf("default Role verbs line = %q, want only get/list/watch", line)
		}
	})
	t.Run("allow_dynamic grants write verbs", func(t *testing.T) {
		bc := newScheduleStageBC(t, &types.ForgeConfig{
			AgentID:   "agent",
			Schedules: []types.ScheduleConfig{{ID: "x", Cron: "@hourly", Task: "t"}},
			Scheduler: types.SchedulerConfig{
				Kubernetes: types.K8sSchedulerConfig{AllowDynamic: true},
			},
		})
		if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		data, _ := os.ReadFile(filepath.Join(bc.Opts.OutputDir, "k8s", "scheduler-role.yaml"))
		line := verbsLine(string(data))
		for _, verb := range []string{"create", "update", "patch", "delete"} {
			if !strings.Contains(line, verb) {
				t.Errorf("allow_dynamic Role verbs line should contain %q; got %q", verb, line)
			}
		}
	})
}

// TestScheduleManifestStage_RoleBindingTargetsAgentSA verifies the
// binding points at the ServiceAccount named after the agent_id —
// the convention forge package's deployment template uses.
func TestScheduleManifestStage_RoleBindingTargetsAgentSA(t *testing.T) {
	bc := newScheduleStageBC(t, &types.ForgeConfig{
		AgentID:   "my-agent",
		Schedules: []types.ScheduleConfig{{ID: "x", Cron: "@hourly", Task: "t"}},
	})
	if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(bc.Opts.OutputDir, "k8s", "scheduler-rolebinding.yaml"))
	body := string(data)
	if !strings.Contains(body, "kind: ServiceAccount") {
		t.Errorf("RoleBinding subject must be a ServiceAccount; got:\n%s", body)
	}
	if !strings.Contains(body, "name: my-agent") {
		t.Errorf("RoleBinding must target ServiceAccount named %q; got:\n%s", "my-agent", body)
	}
	if !strings.Contains(body, "name: my-agent-scheduler") {
		t.Errorf("RoleBinding must reference Role %q; got:\n%s", "my-agent-scheduler", body)
	}
}

// TestScheduleManifestStage_HonorsExplicitServiceURL confirms an
// operator-supplied service_url overrides the in-cluster Service DNS
// default. Used for non-default port / namespace / Ingress deploys.
func TestScheduleManifestStage_HonorsExplicitServiceURL(t *testing.T) {
	bc := newScheduleStageBC(t, &types.ForgeConfig{
		AgentID:   "agent",
		Schedules: []types.ScheduleConfig{{ID: "x", Cron: "@hourly", Task: "t"}},
		Scheduler: types.SchedulerConfig{
			Kubernetes: types.K8sSchedulerConfig{
				ServiceURL: "https://agent.example.com/",
			},
		},
	})
	if err := (&ScheduleManifestStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(bc.Opts.OutputDir, "k8s", "cronjob-x.yaml"))
	if !strings.Contains(string(data), "https://agent.example.com/") {
		t.Errorf("explicit service_url not honored in cronjob-x.yaml:\n%s", data)
	}
	// Default in-cluster DNS must NOT appear.
	if strings.Contains(string(data), "agent.default.svc") {
		t.Errorf("default in-cluster DNS leaked despite explicit service_url:\n%s", data)
	}
}
