package aws_sigv4

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

const validSigv4 = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20260523/us-east-1/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=ab12cd34"

func validHeaders() auth.Headers {
	return auth.Headers{
		"Authorization": validSigv4,
		"X-Amz-Date":    "20260523T120000Z",
	}
}

func newTestProvider(t *testing.T, sts http.Handler, opts ...func(*Config)) *Provider {
	t.Helper()
	srv := httptest.NewServer(sts)
	t.Cleanup(srv.Close)

	cfg := Config{
		Region:           "us-east-1",
		Audience:         "api://forge",
		STSEndpoint:      srv.URL,
		IdentityCacheTTL: 60 * time.Second,
		HTTPTimeout:      5 * time.Second,
	}
	for _, fn := range opts {
		fn(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func happySTS() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, happySTSXML)
	})
}

func TestProvider_Name(t *testing.T) {
	p := newTestProvider(t, happySTS())
	if p.Name() != "aws_sigv4" {
		t.Errorf("Name = %q, want aws_sigv4", p.Name())
	}
}

func TestProvider_New_RequiresRegion(t *testing.T) {
	_, err := New(Config{})
	if err == nil || !errors.Is(err, auth.ErrProviderNotConfigured) {
		t.Errorf("err = %v, want wrapped ErrProviderNotConfigured", err)
	}
}

func TestProvider_New_RejectsInvalidGlob(t *testing.T) {
	_, err := New(Config{Region: "us-east-1", AllowedPrincipals: []string{"["}})
	if err == nil {
		t.Fatal("expected error for malformed glob")
	}
}

func TestProvider_NoSigv4Header_YieldsToChain(t *testing.T) {
	p := newTestProvider(t, happySTS())
	_, err := p.Verify(context.Background(), "", auth.Headers{"Authorization": "Bearer xyz"})
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Errorf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestProvider_NoAuthHeaderAtAll_YieldsToChain(t *testing.T) {
	p := newTestProvider(t, happySTS())
	_, err := p.Verify(context.Background(), "", auth.Headers{})
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Errorf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestProvider_MalformedSigv4_Invalid(t *testing.T) {
	p := newTestProvider(t, happySTS())
	_, err := p.Verify(context.Background(), "", auth.Headers{
		"Authorization": "AWS4-HMAC-SHA256 malformed",
	})
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestProvider_HappyPath_ReturnsIdentity(t *testing.T) {
	p := newTestProvider(t, happySTS())
	id, err := p.Verify(context.Background(), "", validHeaders())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Source != "aws_sigv4" {
		t.Errorf("Source = %q, want aws_sigv4", id.Source)
	}
	if id.UserID != "arn:aws:sts::123456789012:assumed-role/ci-deploy/session" {
		t.Errorf("UserID = %q", id.UserID)
	}
	if id.OrgID != "123456789012" {
		t.Errorf("OrgID = %q", id.OrgID)
	}
	if id.Claims["audience"] != "api://forge" {
		t.Errorf("Claims[audience] = %v", id.Claims["audience"])
	}
}

func TestProvider_ScopeService_NotSTS_Rejected(t *testing.T) {
	p := newTestProvider(t, happySTS())
	h := validHeaders()
	h["Authorization"] = strings.Replace(h["Authorization"], "/sts/", "/s3/", 1)
	_, err := p.Verify(context.Background(), "", h)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected (cross-service replay)", err)
	}
}

func TestProvider_ScopeRegion_Mismatch_Rejected(t *testing.T) {
	p := newTestProvider(t, happySTS())
	h := validHeaders()
	h["Authorization"] = strings.Replace(h["Authorization"], "/us-east-1/", "/eu-west-1/", 1)
	_, err := p.Verify(context.Background(), "", h)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected (cross-region replay)", err)
	}
}

func TestProvider_AllowlistMiss_Rejected(t *testing.T) {
	p := newTestProvider(t, happySTS(), func(c *Config) {
		c.AllowedPrincipals = []string{"arn:aws:iam::999:role/*"}
	})
	_, err := p.Verify(context.Background(), "", validHeaders())
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected (allowlist miss)", err)
	}
}

func TestProvider_AllowlistHit_Succeeds(t *testing.T) {
	p := newTestProvider(t, happySTS(), func(c *Config) {
		// Matches the assumed-role ARN returned by happySTSXML.
		c.AllowedPrincipals = []string{"arn:aws:sts::123456789012:assumed-role/ci-deploy/*"}
	})
	if _, err := p.Verify(context.Background(), "", validHeaders()); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestProvider_STSDown_Unavailable(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := p.Verify(context.Background(), "", validHeaders())
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestProvider_STSRejects_Rejected(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<ErrorResponse><Error><Code>SignatureDoesNotMatch</Code></Error></ErrorResponse>")
	}))
	_, err := p.Verify(context.Background(), "", validHeaders())
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestProvider_CacheHit_AvoidsSTSCall(t *testing.T) {
	var calls atomic.Int32
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, happySTSXML)
	}))

	if _, err := p.Verify(context.Background(), "", validHeaders()); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if _, err := p.Verify(context.Background(), "", validHeaders()); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("STS calls = %d, want 1 (cache must hit on 2nd request)", got)
	}
}

func TestProvider_RejectedRequest_DoesNotPoisonCache(t *testing.T) {
	// First STS call returns 403; second valid request must hit STS again
	// (the rejection MUST NOT have populated the cache).
	var calls atomic.Int32
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<ErrorResponse/>")
			return
		}
		_, _ = io.WriteString(w, happySTSXML)
	}))

	_, err1 := p.Verify(context.Background(), "", validHeaders())
	if !errors.Is(err1, auth.ErrTokenRejected) {
		t.Fatalf("first Verify err = %v, want ErrTokenRejected", err1)
	}
	_, err2 := p.Verify(context.Background(), "", validHeaders())
	if err2 != nil {
		t.Fatalf("second Verify err = %v, want nil", err2)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("STS calls = %d, want 2 (rejection must not poison cache)", got)
	}
}

func TestProvider_DateBucketRollover_TriggersFreshSTSCall(t *testing.T) {
	var calls atomic.Int32
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, happySTSXML)
	}))

	// Day 1
	h1 := validHeaders()
	if _, err := p.Verify(context.Background(), "", h1); err != nil {
		t.Fatal(err)
	}

	// Day 2 — same AKID, different date in scope → different cache key.
	h2 := auth.Headers{
		"Authorization": strings.Replace(validSigv4, "/20260523/", "/20260524/", 1),
		"X-Amz-Date":    "20260524T120000Z",
	}
	if _, err := p.Verify(context.Background(), "", h2); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("STS calls = %d, want 2 (date bucket rolled, cache miss expected)", got)
	}
}

func TestProvider_AmzSecurityTokenForwarded(t *testing.T) {
	var capturedSec string
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSec = r.Header.Get("X-Amz-Security-Token")
		_, _ = io.WriteString(w, happySTSXML)
	}))

	h := validHeaders()
	h["X-Amz-Security-Token"] = "FwoGZX-test-temp-session"
	if _, err := p.Verify(context.Background(), "", h); err != nil {
		t.Fatal(err)
	}
	if capturedSec != "FwoGZX-test-temp-session" {
		t.Errorf("STS got X-Amz-Security-Token = %q", capturedSec)
	}
}

func TestProvider_RegisteredInRegistry(t *testing.T) {
	// init() ran on package import — verify the factory is wired.
	p, err := auth.Build("aws_sigv4", map[string]any{
		"region": "us-east-1",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "aws_sigv4" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestProvider_FactoryRejectsMissingRegion(t *testing.T) {
	_, err := auth.Build("aws_sigv4", map[string]any{})
	if err == nil {
		t.Fatal("expected error from factory when region is missing")
	}
}
