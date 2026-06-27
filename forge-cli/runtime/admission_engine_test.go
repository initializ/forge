package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// admissionTestLogger captures Warn/Error/Info calls so the test can
// assert on the fail-open warn line shape and on the partial-config
// startup warnings.
type admissionTestLogger struct {
	warns  []string
	errors []string
	infos  []string
}

func (l *admissionTestLogger) Debug(string, map[string]any) {}
func (l *admissionTestLogger) Info(msg string, _ map[string]any) {
	l.infos = append(l.infos, msg)
}
func (l *admissionTestLogger) Warn(msg string, _ map[string]any) {
	l.warns = append(l.warns, msg)
}
func (l *admissionTestLogger) Error(msg string, _ map[string]any) {
	l.errors = append(l.errors, msg)
}

// TestPlatformAdmissionChecker_AdmitFromPlatform pins the success
// path: a 200 from the platform with decision=admit produces an
// Allowed=true Decision with the platform-provided reason / scope /
// window / reset_at fields carried through verbatim. The Fallback
// flag is false because the platform actually returned an admit.
func TestPlatformAdmissionChecker_AdmitFromPlatform(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.URL.Query().Get("agent_id"); got != "agent-X" {
			t.Errorf("agent_id query = %q, want agent-X", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		_, _ = w.Write([]byte(`{"decision":"admit"}`))
	}))
	defer srv.Close()

	checker := NewPlatformAdmissionChecker(srv.URL, "agent-X", "", "", "test-token", nil)
	d := checker.Admit(context.Background())

	if !d.Allowed {
		t.Errorf("Allowed = false, want true")
	}
	if d.Fallback {
		t.Errorf("Fallback = true, want false on a real platform admit")
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 platform call, got %d", calls.Load())
	}
}

// TestPlatformAdmissionChecker_DenyFromPlatform pins the deny path —
// the platform-provided reason / scope / window / reset_at fields
// all surface on the Decision unchanged. This is the contract a SIEM
// dashboard depends on for "denials by window type" rollups.
func TestPlatformAdmissionChecker_DenyFromPlatform(t *testing.T) {
	resetAt := time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := admissionResponse{
			Decision: "deny",
			Reason:   "cost_limit_exceeded",
			Scope:    "workspace",
			Window:   "daily",
			ResetAt:  resetAt,
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	checker := NewPlatformAdmissionChecker(srv.URL, "agent-X", "", "", "tok", nil)
	d := checker.Admit(context.Background())

	if d.Allowed {
		t.Errorf("Allowed = true, want false")
	}
	if d.Reason != "cost_limit_exceeded" || d.Scope != "workspace" || d.Window != "daily" {
		t.Errorf("decision fields not carried verbatim: %+v", d)
	}
	if !d.ResetAt.Equal(resetAt) {
		t.Errorf("ResetAt = %v, want %v", d.ResetAt, resetAt)
	}
}

// TestPlatformAdmissionChecker_TenancyHeadersSentAndOmitted is the
// #201 wire-contract pin. Org-Id and Workspace-Id are sent when the
// env values are non-empty; OMITTED entirely (never `Org-Id: ""`)
// when empty so the platform parser distinguishes "self-hosted
// without tenancy" from "platform deploy with malformed tenancy".
func TestPlatformAdmissionChecker_TenancyHeadersSentAndOmitted(t *testing.T) {
	t.Run("both_set", func(t *testing.T) {
		var gotHeaders http.Header
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeaders = r.Header.Clone()
			_, _ = w.Write([]byte(`{"decision":"admit"}`))
		}))
		defer srv.Close()
		checker := NewPlatformAdmissionChecker(srv.URL, "ag", "org-7", "ws-3", "tok", nil)
		checker.Admit(context.Background())
		if got := gotHeaders.Get("Org-Id"); got != "org-7" {
			t.Errorf("Org-Id = %q, want org-7", got)
		}
		if got := gotHeaders.Get("Workspace-Id"); got != "ws-3" {
			t.Errorf("Workspace-Id = %q, want ws-3", got)
		}
	})
	t.Run("both_empty_omitted", func(t *testing.T) {
		var gotHeaders http.Header
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeaders = r.Header.Clone()
			_, _ = w.Write([]byte(`{"decision":"admit"}`))
		}))
		defer srv.Close()
		checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", nil)
		checker.Admit(context.Background())
		// http.Header.Values returns nil for an unset key; check via
		// Values() instead of Get() because Get masks "header set
		// to empty string" as identical to "header not set".
		if len(gotHeaders.Values("Org-Id")) != 0 {
			t.Errorf("Org-Id should be omitted when env empty; got %v", gotHeaders.Values("Org-Id"))
		}
		if len(gotHeaders.Values("Workspace-Id")) != 0 {
			t.Errorf("Workspace-Id should be omitted when env empty; got %v", gotHeaders.Values("Workspace-Id"))
		}
	})
}

// TestPlatformAdmissionChecker_CachesWithinTTL — the second call
// within the cache window must NOT hit the platform. The cached
// decision is returned with Cached=true overlaid for span/audit
// visibility.
func TestPlatformAdmissionChecker_CachesWithinTTL(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"decision":"deny","reason":"x"}`))
	}))
	defer srv.Close()

	checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", nil)
	d1 := checker.Admit(context.Background())
	d2 := checker.Admit(context.Background())
	d3 := checker.Admit(context.Background())

	if calls.Load() != 1 {
		t.Errorf("expected 1 platform call across 3 Admit() calls; got %d", calls.Load())
	}
	if d1.Cached {
		t.Errorf("first call should be a fresh fetch, Cached should be false")
	}
	if !d2.Cached || !d3.Cached {
		t.Errorf("subsequent calls within TTL should be Cached=true; got d2=%v d3=%v",
			d2.Cached, d3.Cached)
	}
	// Decision content (Reason, etc.) survives caching unchanged.
	if d2.Reason != "x" || d3.Reason != "x" {
		t.Errorf("cached decision lost Reason: d2=%+v d3=%+v", d2, d3)
	}
}

// TestPlatformAdmissionChecker_CacheExpires uses the injectable
// clock to walk past the TTL and confirm a fresh call is issued.
func TestPlatformAdmissionChecker_CacheExpires(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"decision":"admit"}`))
	}))
	defer srv.Close()

	now := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", nil)
	checker.now = func() time.Time { return now }

	checker.Admit(context.Background())
	now = now.Add(admissionCacheTTL + time.Millisecond)
	checker.Admit(context.Background())

	if calls.Load() != 2 {
		t.Errorf("expected 2 fresh calls across the TTL boundary; got %d", calls.Load())
	}
}

// TestPlatformAdmissionChecker_FailsOpenOnNetworkError pins the
// central #201 contract: any platform-call failure produces an
// Allowed=true + Fallback=true Decision plus a greppable warn log.
// The fallback admit is cached so a platform outage produces ONE
// log line per agent per TTL, not one per request.
func TestPlatformAdmissionChecker_FailsOpenOnNetworkError(t *testing.T) {
	// 127.0.0.1:1 is reliably unreachable on test hosts.
	logger := &admissionTestLogger{}
	checker := NewPlatformAdmissionChecker("http://127.0.0.1:1/admission",
		"ag", "", "", "tok", logger)

	d := checker.Admit(context.Background())

	if !d.Allowed || !d.Fallback {
		t.Errorf("expected Allowed=true Fallback=true on network error; got %+v", d)
	}
	if len(logger.warns) != 1 || !strings.Contains(logger.warns[0], "admission: call failed") {
		t.Errorf("expected one greppable warn line; got %v", logger.warns)
	}
	// Cached fallback admit — second call within TTL hits cache.
	d2 := checker.Admit(context.Background())
	if !d2.Cached {
		t.Errorf("fallback admit should be cached for TTL; got Cached=false")
	}
	if len(logger.warns) != 1 {
		t.Errorf("expected exactly one warn across 2 calls (cache hit on second); got %d",
			len(logger.warns))
	}
}

// TestPlatformAdmissionChecker_FailsOpenOnPlatform5xx — same
// fail-open path for HTTP 5xx. Confirms the failure-classification
// is uniform across error types.
func TestPlatformAdmissionChecker_FailsOpenOnPlatform5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "platform brownout", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	logger := &admissionTestLogger{}
	checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", logger)
	d := checker.Admit(context.Background())

	if !d.Allowed || !d.Fallback {
		t.Errorf("expected Allowed=true Fallback=true on 503; got %+v", d)
	}
	if len(logger.warns) != 1 || !strings.Contains(logger.warns[0], "admission: call failed") {
		t.Errorf("expected warn on 5xx; got %v", logger.warns)
	}
}

// TestPlatformAdmissionChecker_FailsOpenOnAuth4xx — bad token /
// expired token produces 401/403 from the platform. Same fail-open
// posture: log warn + admit + cache. Operator alerts on the warn
// log catch the misconfiguration without breaking traffic.
func TestPlatformAdmissionChecker_FailsOpenOnAuth4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	logger := &admissionTestLogger{}
	checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", logger)
	d := checker.Admit(context.Background())

	if !d.Allowed || !d.Fallback {
		t.Errorf("expected Allowed=true Fallback=true on 401; got %+v", d)
	}
}

// TestPlatformAdmissionChecker_FailsOpenOnMalformedJSON pins the
// parse-error path. A platform that responds with non-JSON or
// JSON-but-missing-decision must trigger the same fallback admit.
func TestPlatformAdmissionChecker_FailsOpenOnMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	logger := &admissionTestLogger{}
	checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", logger)
	d := checker.Admit(context.Background())
	if !d.Allowed || !d.Fallback {
		t.Errorf("expected Allowed=true Fallback=true on parse error; got %+v", d)
	}
}

// TestPlatformAdmissionChecker_FailsOpenOnUnknownDecision — a
// response that parses but carries decision != admit/deny is treated
// as a malformed response (fail-open). Catches platform-side
// regressions that would otherwise silently flip every agent to a
// surprise behavior.
func TestPlatformAdmissionChecker_FailsOpenOnUnknownDecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"decision":"maybe"}`))
	}))
	defer srv.Close()
	logger := &admissionTestLogger{}
	checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", logger)
	d := checker.Admit(context.Background())
	if !d.Allowed || !d.Fallback {
		t.Errorf("expected fallback admit on unknown decision; got %+v", d)
	}
}

// TestPlatformAdmissionChecker_AppendsAgentIDToExistingQuery
// confirms the URL builder preserves existing query params (e.g.
// for canary routing or platform-side feature flags) and just
// appends agent_id. An operator who sets
// FORGE_ADMISSION_URL=https://platform/admission?canary=true
// must continue to see canary=true on every call.
func TestPlatformAdmissionChecker_AppendsAgentIDToExistingQuery(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"decision":"admit"}`))
	}))
	defer srv.Close()
	checker := NewPlatformAdmissionChecker(srv.URL+"?canary=true", "ag-7", "", "", "tok", nil)
	checker.Admit(context.Background())
	if !strings.Contains(seen, "canary=true") || !strings.Contains(seen, "agent_id=ag-7") {
		t.Errorf("query missing one of canary=true / agent_id=ag-7: got %q", seen)
	}
}

// TestBuildAdmissionChecker_BothEnvSetReturnsPlatformChecker is the
// engaged-path pin. With both env vars set the loader returns a
// PlatformAdmissionChecker (not a Noop) and logs the Info line so
// operators see the activation in the agent startup log.
func TestBuildAdmissionChecker_BothEnvSetReturnsPlatformChecker(t *testing.T) {
	t.Setenv(EnvAdmissionURL, "https://platform.example/v1/admission")
	t.Setenv(EnvPlatformToken, "test-token")
	logger := &admissionTestLogger{}

	c := BuildAdmissionChecker("ag", logger)
	if _, noop := c.(coreruntime.NoopAdmissionChecker); noop {
		t.Errorf("expected PlatformAdmissionChecker when both env vars set; got Noop")
	}
	if len(logger.infos) == 0 {
		t.Errorf("expected Info log on engagement; got none")
	}
}

// TestBuildAdmissionChecker_NeitherEnvSetSilentNoop pins the
// default-deploy path: zero env vars produces a silent Noop. The
// loader doesn't log anything because the operator did not opt into
// admission.
func TestBuildAdmissionChecker_NeitherEnvSetSilentNoop(t *testing.T) {
	t.Setenv(EnvAdmissionURL, "")
	t.Setenv(EnvPlatformToken, "")
	logger := &admissionTestLogger{}

	c := BuildAdmissionChecker("ag", logger)
	if _, noop := c.(coreruntime.NoopAdmissionChecker); !noop {
		t.Errorf("expected Noop when neither env set; got %T", c)
	}
	if len(logger.warns) != 0 || len(logger.infos) != 0 {
		t.Errorf("expected silent Noop; got warns=%v infos=%v", logger.warns, logger.infos)
	}
}

// TestBuildAdmissionChecker_PartialConfigWarnsButReturnsNoop pins
// the operator-error case. URL set without TOKEN (or vice versa)
// produces a Noop AND a warn line naming the missing env var, so
// a single-line fix in the deployment manifest is obvious.
func TestBuildAdmissionChecker_PartialConfigWarnsButReturnsNoop(t *testing.T) {
	t.Run("url_without_token", func(t *testing.T) {
		t.Setenv(EnvAdmissionURL, "https://platform.example/admission")
		t.Setenv(EnvPlatformToken, "")
		logger := &admissionTestLogger{}
		c := BuildAdmissionChecker("ag", logger)
		if _, noop := c.(coreruntime.NoopAdmissionChecker); !noop {
			t.Errorf("expected Noop when only URL set; got %T", c)
		}
		if len(logger.warns) != 1 || !strings.Contains(logger.warns[0], "FORGE_PLATFORM_TOKEN") {
			t.Errorf("warn should name the missing FORGE_PLATFORM_TOKEN env; got %v", logger.warns)
		}
	})
	t.Run("token_without_url", func(t *testing.T) {
		t.Setenv(EnvAdmissionURL, "")
		t.Setenv(EnvPlatformToken, "tok")
		logger := &admissionTestLogger{}
		c := BuildAdmissionChecker("ag", logger)
		if _, noop := c.(coreruntime.NoopAdmissionChecker); !noop {
			t.Errorf("expected Noop when only TOKEN set; got %T", c)
		}
		if len(logger.warns) != 1 {
			t.Errorf("expected warn on partial config; got %v", logger.warns)
		}
	})
}

// TestPlatformAdmissionChecker_TimeoutHonored confirms the baked
// 2s timeout fires when the platform hangs. Without the timeout, a
// slow platform would block the inbound A2A request indefinitely.
// Tested with a server that sleeps past the timeout deadline.
func TestPlatformAdmissionChecker_TimeoutHonored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(admissionHTTPTimeout + 500*time.Millisecond)
		_, _ = w.Write([]byte(`{"decision":"deny"}`))
	}))
	defer srv.Close()
	logger := &admissionTestLogger{}
	checker := NewPlatformAdmissionChecker(srv.URL, "ag", "", "", "tok", logger)

	start := time.Now()
	d := checker.Admit(context.Background())
	elapsed := time.Since(start)

	if elapsed > admissionHTTPTimeout+500*time.Millisecond {
		t.Errorf("Admit took %v, expected close to %v (the baked timeout)",
			elapsed, admissionHTTPTimeout)
	}
	if !d.Allowed || !d.Fallback {
		t.Errorf("timeout should fall open; got %+v", d)
	}
}

// keep fmt used so the file compiles if a future test relies on it.
var _ = fmt.Sprintf
