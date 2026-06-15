package scheduler

import (
	"strings"
	"testing"
)

func TestCronJobName_FitsK8sLimits(t *testing.T) {
	tests := []struct {
		name     string
		agent    string
		sched    string
		wantMax  int
		wantHave []string
	}{
		{
			name:     "short fits unchanged",
			agent:    "aibuilderdemo",
			sched:    "daily-summary",
			wantMax:  63,
			wantHave: []string{"forge-aibuilderdemo-daily-summary"},
		},
		{
			name:    "overlong gets hash-suffixed",
			agent:   "really-long-agent-identifier-that-exceeds-the-limit-when-combined",
			sched:   "another-long-schedule-identifier-here",
			wantMax: 63,
		},
		{
			name:     "sanitizes invalid chars",
			agent:    "MyAgent_2",
			sched:    "Daily.Summary",
			wantMax:  63,
			wantHave: []string{"forge-myagent-2-daily-summary"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CronJobName(tt.agent, tt.sched)
			if len(got) > tt.wantMax {
				t.Errorf("CronJobName length %d exceeds K8s limit %d (got %q)", len(got), tt.wantMax, got)
			}
			for _, sub := range tt.wantHave {
				if !strings.Contains(got, sub) {
					t.Errorf("CronJobName(%q, %q) = %q, want substring %q", tt.agent, tt.sched, got, sub)
				}
			}
		})
	}
}

// TestCronJobName_StableHashForUniqueness pins the hash-suffix
// behavior: two distinct (agent, schedule) pairs that share a common
// prefix MUST produce distinct names even after truncation.
func TestCronJobName_StableHashForUniqueness(t *testing.T) {
	longAgent := strings.Repeat("a", 50)
	a := CronJobName(longAgent, "schedule-one-that-is-also-long")
	b := CronJobName(longAgent, "schedule-two-that-is-also-long")
	if a == b {
		t.Errorf("distinct schedule IDs produced colliding CronJob names: both %q", a)
	}
}

// TestCronJobYAML_HasRequiredFields verifies the manifest text
// contains the structural elements operators care about:
// concurrencyPolicy=Forbid (the K8s-native equivalent of the file
// backend's overlap skip), the schedule cron, the X-Forge-Schedule-Id
// header (drives audit-event linkage in #162 part 3), and the task
// text from the Schedule.
func TestCronJobYAML_HasRequiredFields(t *testing.T) {
	yaml := CronJobYAML(CronJobManifestInput{
		AgentID:    "aibuilderdemo",
		Namespace:  "default",
		ServiceURL: "http://aibuilderdemo.default.svc:8080/",
		Schedule: Schedule{
			ID:     "daily-summary",
			Cron:   "0 9 * * *",
			Task:   "Send the daily summary to Slack #ops",
			Source: SourceYAML,
		},
	})

	want := []string{
		"kind: CronJob",
		"schedule: \"0 9 * * *\"",
		"concurrencyPolicy: Forbid",
		"X-Forge-Schedule-Id: daily-summary",
		"forge.agent.id: aibuilderdemo",
		"forge.schedule.id: daily-summary",
		"forge.schedule.source: yaml",
		"Send the daily summary to Slack #ops",
		"http://aibuilderdemo.default.svc:8080/",
	}
	for _, sub := range want {
		if !strings.Contains(yaml, sub) {
			t.Errorf("CronJobYAML missing %q\nfull output:\n%s", sub, yaml)
		}
	}
}

// TestCronJobYAML_DefaultsAppliedForMissingFields verifies the
// defaults the manifest builder fills in when caller leaves fields
// empty: trigger image, auth secret name, namespace, source. Lets
// `forge package` emit a CronJob from minimal forge.yaml input.
func TestCronJobYAML_DefaultsAppliedForMissingFields(t *testing.T) {
	yaml := CronJobYAML(CronJobManifestInput{
		AgentID:    "agent",
		ServiceURL: "http://agent.default.svc:8080/",
		Schedule:   Schedule{ID: "x", Cron: "@hourly", Task: "do x"},
	})

	want := []string{
		"image: " + DefaultTriggerImage,
		"name: agent-internal-token",
		"namespace: default",
		"forge.schedule.source: yaml",
	}
	for _, sub := range want {
		if !strings.Contains(yaml, sub) {
			t.Errorf("missing default %q\n%s", sub, yaml)
		}
	}
}

// TestCronJobYAML_EscapesTaskSingleQuotes pins the shell-quoting
// behavior: task text containing a single quote must not break out
// of the single-quoted JSON body. Without the escape the curl args
// line would be unparseable shell.
func TestCronJobYAML_EscapesTaskSingleQuotes(t *testing.T) {
	yaml := CronJobYAML(CronJobManifestInput{
		AgentID:    "agent",
		ServiceURL: "http://agent.svc/",
		Schedule:   Schedule{ID: "x", Cron: "@hourly", Task: "what's up"},
	})
	// The escaped pattern '"'"' is the standard sh idiom for an
	// embedded single quote inside single-quoted text.
	if !strings.Contains(yaml, `what'"'"'s up`) {
		t.Errorf("single quote not shell-escaped\n%s", yaml)
	}
}

// TestInCluster_FORGEEnvOverride verifies the test-only escape hatch
// works in both directions. The actual on-cluster signal
// (/var/run/secrets/...) can't be exercised in CI, but the env
// override is the path tests + ops use to force a mode.
func TestInCluster_FORGEEnvOverride(t *testing.T) {
	t.Setenv("FORGE_IN_CLUSTER", "true")
	if !InCluster() {
		t.Error("FORGE_IN_CLUSTER=true should force InCluster true")
	}
	t.Setenv("FORGE_IN_CLUSTER", "false")
	if InCluster() {
		t.Error("FORGE_IN_CLUSTER=false should force InCluster false")
	}
}
