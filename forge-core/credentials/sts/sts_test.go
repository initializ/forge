package sts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/credentials"
)

// mockSTS returns a test server that impersonates STS AssumeRole.
// Captures the request headers + body for assertions.
type mockSTS struct {
	srv          *httptest.Server
	lastAuth     string
	lastDate     string
	lastToken    string
	lastBody     string
	responseBody string
	responseCode int
	callCount    int // number of AssumeRole POSTs received
}

func newMockSTS() *mockSTS {
	m := &mockSTS{
		responseCode: 200,
		responseBody: `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>ASIAMOCKEXAMPLE</AccessKeyId>
      <SecretAccessKey>mockSecretAccessKeyMockSecretAccessKey0</SecretAccessKey>
      <SessionToken>mockSessionTokenMockSessionToken==</SessionToken>
      <Expiration>2099-01-01T00:00:00Z</Expiration>
    </Credentials>
  </AssumeRoleResult>
</AssumeRoleResponse>`,
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.callCount++
		m.lastAuth = r.Header.Get("Authorization")
		m.lastDate = r.Header.Get("X-Amz-Date")
		m.lastToken = r.Header.Get("X-Amz-Security-Token")
		b, _ := io.ReadAll(r.Body)
		m.lastBody = string(b)
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(m.responseCode)
		_, _ = w.Write([]byte(m.responseBody))
	}))
	return m
}

// setSTSEnv sets AWS_* source-creds env for the test.
func setSTSEnv(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIASOURCE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sourceSecretKey")
	t.Setenv("AWS_SESSION_TOKEN", "sourceSessionToken")
}

func TestSTSProvider_RegisteredAtInit(t *testing.T) {
	if p := credentials.DefaultRegistry.Get(ProviderName); p == nil {
		t.Fatal("sts_assume_role not registered")
	}
}

func TestSTSProvider_MaterializeHappyPath(t *testing.T) {
	setSTSEnv(t)
	m := newMockSTS()
	defer m.srv.Close()

	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec: mustJSON(t, Spec{
			RoleARN:     "arn:aws:iam::123456789012:role/skill-read",
			SessionName: "skill-alpha",
			ExternalID:  "ext-42",
			Duration:    "15m",
			Endpoint:    m.srv.URL + "/",
		}),
	}
	p := Provider{HTTPClient: m.srv.Client()}
	cred, err := p.NewCredential(context.Background(), spec)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	mat, err := cred.Materialize(context.Background(), "cli_execute", nil)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if mat.Env["AWS_ACCESS_KEY_ID"] != "ASIAMOCKEXAMPLE" {
		t.Errorf("access key not surfaced: %v", mat.Env)
	}
	if mat.Env["AWS_SECRET_ACCESS_KEY"] == "" || mat.Env["AWS_SESSION_TOKEN"] == "" {
		t.Errorf("missing secret/token: %v", mat.Env)
	}
	if mat.TTL == "" {
		t.Error("TTL should be set")
	}
}

func TestSTSProvider_RequestFormAndSignature(t *testing.T) {
	setSTSEnv(t)
	m := newMockSTS()
	defer m.srv.Close()

	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec: mustJSON(t, Spec{
			RoleARN:     "arn:aws:iam::123456789012:role/skill-read",
			SessionName: "skill-alpha",
			ExternalID:  "ext-42",
			Duration:    "30m",
			Endpoint:    m.srv.URL + "/",
		}),
	}
	p := Provider{HTTPClient: m.srv.Client()}
	cred, err := p.NewCredential(context.Background(), spec)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	if _, err := cred.Materialize(context.Background(), "", nil); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if !strings.Contains(m.lastBody, "Action=AssumeRole") {
		t.Errorf("body missing Action: %s", m.lastBody)
	}
	if !strings.Contains(m.lastBody, "RoleArn=arn%3Aaws%3Aiam%3A%3A123456789012%3Arole%2Fskill-read") {
		t.Errorf("body missing RoleArn: %s", m.lastBody)
	}
	if !strings.Contains(m.lastBody, "ExternalId=ext-42") {
		t.Errorf("body missing ExternalId: %s", m.lastBody)
	}
	if !strings.Contains(m.lastBody, "DurationSeconds=1800") {
		t.Errorf("body missing DurationSeconds=1800: %s", m.lastBody)
	}
	if !strings.HasPrefix(m.lastAuth, "AWS4-HMAC-SHA256 Credential=AKIASOURCE/") {
		t.Errorf("Authorization malformed: %q", m.lastAuth)
	}
	if !strings.Contains(m.lastAuth, "/sts/aws4_request") {
		t.Errorf("Authorization missing service scope: %q", m.lastAuth)
	}
	if m.lastToken != "sourceSessionToken" {
		t.Errorf("session token not forwarded: %q", m.lastToken)
	}
	if m.lastDate == "" {
		t.Error("X-Amz-Date missing")
	}
}

func TestSTSProvider_STSErrorSurfaces(t *testing.T) {
	setSTSEnv(t)
	m := newMockSTS()
	m.responseCode = 403
	m.responseBody = `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <Error>
    <Code>AccessDenied</Code>
    <Message>User is not authorized to perform: sts:AssumeRole</Message>
  </Error>
</ErrorResponse>`
	defer m.srv.Close()

	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec: mustJSON(t, Spec{
			RoleARN:  "arn:aws:iam::123:role/x",
			Endpoint: m.srv.URL + "/",
		}),
	}
	p := Provider{HTTPClient: m.srv.Client()}
	cred, _ := p.NewCredential(context.Background(), spec)
	_, err := cred.Materialize(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error on STS 403")
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("error should mention STS error code: %v", err)
	}
}

func TestSTSProvider_MissingRoleARNRejectedAtConfig(t *testing.T) {
	p := Provider{}
	_, err := p.NewCredential(context.Background(), credentials.CredentialSpec{
		Provider: ProviderName,
		Spec:     mustJSON(t, Spec{Duration: "15m"}),
	})
	if err == nil {
		t.Fatal("expected error when role_arn is missing")
	}
}

func TestSTSProvider_DurationValidation(t *testing.T) {
	cases := []struct {
		name    string
		dur     string
		wantErr bool
	}{
		{"exactly 15m OK", "15m", false},
		{"1h OK", "1h", false},
		{"12h OK", "12h", false},
		{"below 15m rejected", "5m", true},
		{"above 12h rejected", "13h", true},
		{"garbage rejected", "not-a-duration", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Provider{}.NewCredential(context.Background(), credentials.CredentialSpec{
				Provider: ProviderName,
				Spec:     mustJSON(t, Spec{RoleARN: "arn:aws:iam::1:role/x", Duration: c.dur}),
			})
			if (err != nil) != c.wantErr {
				t.Errorf("dur=%s err=%v wantErr=%v", c.dur, err, c.wantErr)
			}
		})
	}
}

func TestSTSProvider_MissingSourceCredsErrors(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec:     mustJSON(t, Spec{RoleARN: "arn:aws:iam::1:role/x"}),
	}
	cred, err := Provider{}.NewCredential(context.Background(), spec)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	_, err = cred.Materialize(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error when source creds unset")
	}
	if !strings.Contains(err.Error(), "source creds missing") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestSTSProvider_EmptyResponseCreds(t *testing.T) {
	setSTSEnv(t)
	m := newMockSTS()
	m.responseBody = `<AssumeRoleResponse><AssumeRoleResult><Credentials></Credentials></AssumeRoleResult></AssumeRoleResponse>`
	defer m.srv.Close()

	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec:     mustJSON(t, Spec{RoleARN: "arn:aws:iam::1:role/x", Endpoint: m.srv.URL + "/"}),
	}
	p := Provider{HTTPClient: m.srv.Client()}
	cred, _ := p.NewCredential(context.Background(), spec)
	_, err := cred.Materialize(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error on empty creds response")
	}
}

// TestSTSProvider_MaterializeCachesWithinTTL is the regression test
// for reviewer @initializ-mk's #236 fix #2: repeated Materialize calls
// within the STS credential's TTL must return the cached credential
// rather than issuing a fresh AssumeRole. Pre-fix, an agent running
// `aws` N times made N STS calls, adding latency and throttle risk.
func TestSTSProvider_MaterializeCachesWithinTTL(t *testing.T) {
	setSTSEnv(t)
	m := newMockSTS()
	defer m.srv.Close()

	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec: mustJSON(t, Spec{
			RoleARN:  "arn:aws:iam::1:role/x",
			Duration: "15m",
			Endpoint: m.srv.URL + "/",
		}),
	}
	p := Provider{HTTPClient: m.srv.Client()}
	cred, err := p.NewCredential(context.Background(), spec)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}

	// Materialize five times.
	var lastKey string
	for i := range 5 {
		mat, err := cred.Materialize(context.Background(), "cli_execute", nil)
		if err != nil {
			t.Fatalf("Materialize %d: %v", i, err)
		}
		if mat.Env["AWS_ACCESS_KEY_ID"] == "" {
			t.Fatalf("Materialize %d: missing key", i)
		}
		lastKey = mat.Env["AWS_ACCESS_KEY_ID"]
	}
	if lastKey != "ASIAMOCKEXAMPLE" {
		t.Errorf("materialized access key: got %q", lastKey)
	}

	// mock STS should have been hit ONCE — subsequent calls served
	// from cache. Assertion covered by mockSTS.callCount below.
	if m.callCount != 1 {
		t.Errorf("STS AssumeRole call count: got %d, want 1 (5 Materialize → 1 cold + 4 cached)", m.callCount)
	}
}

// TestSTSProvider_ExpiredCacheReMints proves the cache respects
// expiration — pushing the clock past expiration triggers a re-mint.
func TestSTSProvider_ExpiredCacheReMints(t *testing.T) {
	setSTSEnv(t)
	m := newMockSTS()
	defer m.srv.Close()

	// Emit an STS response with a fake Expiration in the near future.
	m.responseBody = `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>ASIAMOCKEXAMPLE</AccessKeyId>
      <SecretAccessKey>mockSecretKey</SecretAccessKey>
      <SessionToken>mockToken</SessionToken>
      <Expiration>2026-07-06T12:15:00Z</Expiration>
    </Credentials>
  </AssumeRoleResult>
</AssumeRoleResponse>`

	// Simulate a clock — start before expiration, advance past it.
	fakeClock := mustTime("2026-07-06T12:00:00Z")
	p := Provider{
		HTTPClient: m.srv.Client(),
		Now:        func() time.Time { return fakeClock },
	}
	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec: mustJSON(t, Spec{
			RoleARN:  "arn:aws:iam::1:role/x",
			Duration: "15m",
			Endpoint: m.srv.URL + "/",
		}),
	}
	cred, err := p.NewCredential(context.Background(), spec)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	// Cold call.
	if _, err := cred.Materialize(context.Background(), "", nil); err != nil {
		t.Fatalf("cold: %v", err)
	}
	// Advance past cached expiration (12:15 - 60s skew = 12:14) →
	// cache stale, must re-mint.
	fakeClock = mustTime("2026-07-06T12:14:30Z")
	if _, err := cred.Materialize(context.Background(), "", nil); err != nil {
		t.Fatalf("re-mint: %v", err)
	}
	if m.callCount != 2 {
		t.Errorf("STS AssumeRole call count: got %d, want 2 (cold + re-mint after expiry)", m.callCount)
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
