package aws_sigv4

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// fixedTestTime is the wall-clock the test helpers pretend it is.
// Tokens minted by tokenFor() are signed at this instant (X-Amz-Date)
// and Provider.now is pinned to this instant via newTestProvider, so
// CheckFreshness sees an in-window token by default. Tests that want
// to exercise expiry/skew override Provider.now after construction.
var fixedTestTime = time.Date(2026, 5, 24, 1, 0, 0, 0, time.UTC)

// tokenFor builds a forge-aws-v1 token whose embedded URL points at the
// test STS server. AKID + date + region + signature are placeholders;
// the fake STS doesn't validate them. X-Amz-Date is pinned to
// fixedTestTime so the freshness check passes by default.
func tokenFor(stsURL, akid, dateYYYYMMDD, region string) string {
	q := url.Values{}
	q.Set("Action", "GetCallerIdentity")
	q.Set("Version", "2011-06-15")
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", fmt.Sprintf("%s/%s/%s/sts/aws4_request", akid, dateYYYYMMDD, region))
	// Build X-Amz-Date from the (possibly day-rolled) dateYYYYMMDD plus
	// fixedTestTime's HHMMSS so date-bucket rollover tests still work.
	q.Set("X-Amz-Date", dateYYYYMMDD+"T"+fixedTestTime.UTC().Format("150405")+"Z")
	q.Set("X-Amz-Expires", "900")
	q.Set("X-Amz-SignedHeaders", "host")
	q.Set("X-Amz-Signature", "fakesig"+akid)
	full := stsURL + "/?" + q.Encode()
	return TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(full))
}

func defaultToken(stsURL string) string {
	return tokenFor(stsURL, "AKIAIOSFODNN7EXAMPLE", "20260524", "us-east-1")
}

func newTestProvider(t *testing.T, sts http.Handler, opts ...func(*Config)) (*Provider, string) {
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
	// Pin the provider's clock to fixedTestTime so tokens minted by
	// tokenFor() pass the M2 freshness check.
	p.now = func() time.Time { return fixedTestTime }
	return p, srv.URL
}

func happySTS() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, happySTSXML)
	})
}

func TestProvider_Name(t *testing.T) {
	p, _ := newTestProvider(t, happySTS())
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

func TestProvider_NoPrefix_YieldsToChain(t *testing.T) {
	p, _ := newTestProvider(t, happySTS())
	_, err := p.Verify(context.Background(), "Bearer some.opaque.token", nil)
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Errorf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestProvider_EmptyToken_YieldsToChain(t *testing.T) {
	p, _ := newTestProvider(t, happySTS())
	_, err := p.Verify(context.Background(), "", nil)
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Errorf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestProvider_MalformedToken_Invalid(t *testing.T) {
	p, _ := newTestProvider(t, happySTS())
	_, err := p.Verify(context.Background(), TokenPrefix+"!!!not-base64!!!", nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestProvider_HappyPath_ReturnsIdentity(t *testing.T) {
	p, stsURL := newTestProvider(t, happySTS())
	id, err := p.Verify(context.Background(), defaultToken(stsURL), nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Source != "aws_sigv4" {
		t.Errorf("Source = %q", id.Source)
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

func TestProvider_RegionMismatch_Rejected(t *testing.T) {
	// Token's credential scope says eu-west-1, provider configured us-east-1.
	// Defends against cross-region token replay (the same AKID may be
	// valid in either region, but the operator's allowlist applies only
	// to the configured region).
	p, stsURL := newTestProvider(t, happySTS())
	tok := tokenFor(stsURL, "AKIAEXAMPLE", "20260524", "eu-west-1")
	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected (cross-region)", err)
	}
}

func TestProvider_ForeignHost_Invalid(t *testing.T) {
	// SSRF guard: a token whose URL points anywhere other than the
	// expected STS host is rejected before we ever issue a request.
	p, _ := newTestProvider(t, happySTS())
	hostile := "https://evil.example.com/?Action=GetCallerIdentity" +
		"&X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIA/20260524/us-east-1/sts/aws4_request" +
		"&X-Amz-Signature=x"
	tok := TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(hostile))
	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (SSRF guard)", err)
	}
}

func TestProvider_AllowlistMiss_Rejected(t *testing.T) {
	p, stsURL := newTestProvider(t, happySTS(), func(c *Config) {
		c.AllowedPrincipals = []string{"arn:aws:iam::999:role/*"}
	})
	_, err := p.Verify(context.Background(), defaultToken(stsURL), nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected (allowlist miss)", err)
	}
}

func TestProvider_AllowlistHit_Succeeds(t *testing.T) {
	p, stsURL := newTestProvider(t, happySTS(), func(c *Config) {
		c.AllowedPrincipals = []string{"arn:aws:sts::123456789012:assumed-role/ci-deploy/*"}
	})
	if _, err := p.Verify(context.Background(), defaultToken(stsURL), nil); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestProvider_AllowedAccounts_AllowsAnyIdentityInAccount(t *testing.T) {
	// The fake STS returns assumed-role ARN for account 123456789012.
	// AllowedAccounts=[123456789012] expands to globs covering all
	// identity shapes in that account → the assumed-role ARN matches.
	p, stsURL := newTestProvider(t, happySTS(), func(c *Config) {
		c.AllowedAccounts = []string{"123456789012"}
	})
	if _, err := p.Verify(context.Background(), defaultToken(stsURL), nil); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestProvider_AllowedAccounts_DifferentAccountRejected(t *testing.T) {
	p, stsURL := newTestProvider(t, happySTS(), func(c *Config) {
		c.AllowedAccounts = []string{"999999999999"}
	})
	_, err := p.Verify(context.Background(), defaultToken(stsURL), nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestProvider_AllowedAccounts_RejectsMalformedAtFactory(t *testing.T) {
	_, err := New(Config{
		Region:          "us-east-1",
		AllowedAccounts: []string{"not-an-account"},
	})
	if err == nil {
		t.Fatal("expected error on malformed account ID")
	}
}

func TestProvider_AllowedAccounts_MergesWithAllowedPrincipals(t *testing.T) {
	// Mix: account-wide grant for 123456789012 (covers the test STS
	// response) + a specific role pattern for some other account.
	// Verify the account-wide entry takes precedence.
	p, stsURL := newTestProvider(t, happySTS(), func(c *Config) {
		c.AllowedPrincipals = []string{"arn:aws:iam::999:role/specific"}
		c.AllowedAccounts = []string{"123456789012"}
	})
	if _, err := p.Verify(context.Background(), defaultToken(stsURL), nil); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestProvider_STSDown_Unavailable(t *testing.T) {
	p, stsURL := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := p.Verify(context.Background(), defaultToken(stsURL), nil)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestProvider_STSRejects_Rejected(t *testing.T) {
	p, stsURL := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<ErrorResponse><Error><Code>SignatureDoesNotMatch</Code></Error></ErrorResponse>")
	}))
	_, err := p.Verify(context.Background(), defaultToken(stsURL), nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestProvider_CacheHit_AvoidsSTSCall(t *testing.T) {
	var calls atomic.Int32
	p, stsURL := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, happySTSXML)
	}))
	tok := defaultToken(stsURL)
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("STS calls = %d, want 1 (cache must hit)", got)
	}
}

func TestProvider_RejectedRequest_DoesNotPoisonCache(t *testing.T) {
	var calls atomic.Int32
	p, stsURL := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<ErrorResponse/>")
			return
		}
		_, _ = io.WriteString(w, happySTSXML)
	}))
	tok := defaultToken(stsURL)
	_, err1 := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err1, auth.ErrTokenRejected) {
		t.Fatalf("first Verify err = %v, want ErrTokenRejected", err1)
	}
	_, err2 := p.Verify(context.Background(), tok, nil)
	if err2 != nil {
		t.Fatalf("second Verify err = %v, want nil", err2)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("STS calls = %d, want 2 (rejection must not poison cache)", got)
	}
}

func TestProvider_DateBucketRollover_TriggersFreshSTSCall(t *testing.T) {
	var calls atomic.Int32
	p, stsURL := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, happySTSXML)
	}))
	// Day 1: use the default fixedTestTime clock.
	if _, err := p.Verify(context.Background(), tokenFor(stsURL, "AKIA", "20260524", "us-east-1"), nil); err != nil {
		t.Fatal(err)
	}
	// Day 2: advance clock by 24h so the day-2 token is fresh under
	// CheckFreshness (post-M2 freshness gate).
	p.now = func() time.Time { return fixedTestTime.Add(24 * time.Hour) }
	if _, err := p.Verify(context.Background(), tokenFor(stsURL, "AKIA", "20260525", "us-east-1"), nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("STS calls = %d, want 2 (date bucket rolled)", got)
	}
}

// --- Review M2: parser-side freshness ---

func TestProvider_RejectsExpiredToken(t *testing.T) {
	// Token's X-Amz-Date + Expires window is in the past relative to
	// Provider.now. STS would also reject this, but we belt-and-brace.
	p, stsURL := newTestProvider(t, happySTS())
	p.now = func() time.Time {
		return fixedTestTime.Add(30 * time.Minute) // far past the 15min lifetime + 5min skew
	}
	_, err := p.Verify(context.Background(), defaultToken(stsURL), nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
	if err != nil && !strings.Contains(err.Error(), "expired") {
		t.Errorf("err should mention 'expired'; got %v", err)
	}
}

func TestProvider_RejectsTokenFromFuture(t *testing.T) {
	// Token signed beyond skew tolerance in the future.
	p, stsURL := newTestProvider(t, happySTS())
	p.now = func() time.Time {
		return fixedTestTime.Add(-1 * time.Hour) // way before the token's signing instant
	}
	_, err := p.Verify(context.Background(), defaultToken(stsURL), nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
	if err != nil && !strings.Contains(err.Error(), "future") {
		t.Errorf("err should mention 'future'; got %v", err)
	}
}

func TestProvider_RejectsOverlyLongExpiresClaim(t *testing.T) {
	// Caller crafted a token with X-Amz-Expires > 15min. STS would
	// also reject the signature, but we belt-and-brace at parse-side.
	p, stsURL := newTestProvider(t, happySTS())

	// Build the token pointing at the test STS host so we pass the
	// SSRF-guard host check; the freshness check then catches the
	// oversized X-Amz-Expires.
	q := url.Values{}
	q.Set("Action", "GetCallerIdentity")
	q.Set("Version", "2011-06-15")
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", "AKIA/20260524/us-east-1/sts/aws4_request")
	q.Set("X-Amz-Date", "20260524T010000Z")
	q.Set("X-Amz-Expires", "3600") // 1 hour — exceeds our 15min cap
	q.Set("X-Amz-SignedHeaders", "host")
	q.Set("X-Amz-Signature", "abc")
	full := stsURL + "/?" + q.Encode()
	tok := TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(full))

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
	if err != nil && !strings.Contains(err.Error(), "X-Amz-Expires") {
		t.Errorf("err should mention X-Amz-Expires cap; got %v", err)
	}
}

func TestProvider_AcceptsTokenAtEdgeOfSkewWindow(t *testing.T) {
	// Just barely fresh: signed at fixedTestTime, expires after 15min,
	// and now is fixedTestTime + 15min + 4min skew (still within 5min).
	p, stsURL := newTestProvider(t, happySTS())
	p.now = func() time.Time {
		return fixedTestTime.Add(15*time.Minute + 4*time.Minute)
	}
	if _, err := p.Verify(context.Background(), defaultToken(stsURL), nil); err != nil {
		t.Errorf("token within skew window should pass, got %v", err)
	}
}

func TestProvider_RegisteredInRegistry(t *testing.T) {
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

func TestProvider_TokenPointingAtForeignSTSRegion_Invalid(t *testing.T) {
	// Token URL says sts.eu-west-1.amazonaws.com — provider expects
	// sts.us-east-1.amazonaws.com. The pre-validation host check should
	// catch this before any STS call.
	p, _ := newTestProvider(t, happySTS(), func(c *Config) {
		// Use defaults so expectedHost stays sts.us-east-1.amazonaws.com.
		// Drop STSEndpoint override to exercise the real host path.
		c.STSEndpoint = ""
	})
	// Force the provider to compare against the real sts host.
	hostile := "https://sts.eu-west-1.amazonaws.com/?Action=GetCallerIdentity" +
		"&X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIA/20260524/eu-west-1/sts/aws4_request" +
		"&X-Amz-Signature=x"
	tok := TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(hostile))
	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (cross-region URL host)", err)
	}
}

// Sanity: with no STSEndpoint override the expectedHost matches AWS's
// real STS endpoint for the configured region — protects against
// accidental regressions to the prod host derivation.
func TestProvider_DefaultExpectedHost(t *testing.T) {
	p, err := New(Config{Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.expectedHost != "sts.us-east-1.amazonaws.com" {
		t.Errorf("expectedHost = %q", p.expectedHost)
	}
	if !p.requireHTTPS {
		t.Error("requireHTTPS should be true by default")
	}
}

// startsWithBearer is a tiny helper for the token_kind tests in middleware
// (kept local so a test-only constant doesn't leak into the production package).
var _ = strings.HasPrefix
