// Package sts is the AWS STS AssumeRole reference provider for
// governance R9 (JIT credential dispensing).
//
// One provider instance can serve many CredentialSpecs. Each spec
// resolves to a Credential closed over the target role ARN, session
// name, duration, and optional external-id / session policy. Every
// Materialize call issues a fresh STS AssumeRole → returns
// short-lived AWS_* env vars in the Materialization → the runner
// injects them into the tool's subprocess env.
//
// SDK-free: STS AssumeRole is a single Query-API POST signed with
// SigV4. The signer here is a stripped-down copy of the Bedrock
// signer in forge-core/llm/providers/sigv4_transport.go — Forge's
// intentional pattern is to hand-roll narrow AWS calls rather than
// pull the full aws-sdk-go-v2 (~5 MB binary blow-up) for one
// endpoint. See docs/security/least-privilege-credentials.md.
package sts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/credentials"
)

// ProviderName is the string used in CredentialSpec.Provider.
const ProviderName = "sts_assume_role"

// Spec is decoded from CredentialSpec.Spec.
type Spec struct {
	RoleARN         string `json:"role_arn"`
	SessionName     string `json:"session_name,omitempty"`
	ExternalID      string `json:"external_id,omitempty"`
	Duration        string `json:"duration,omitempty"`          // e.g. "15m", "1h"; default 15m
	SessionPolicy   string `json:"session_policy,omitempty"`    // inline JSON, sent as Policy=
	Region          string `json:"region,omitempty"`            // default us-east-1
	Endpoint        string `json:"endpoint,omitempty"`          // override for tests
	SourceAccessKey string `json:"source_access_key,omitempty"` // env var name; default AWS_ACCESS_KEY_ID
	SourceSecretKey string `json:"source_secret_key,omitempty"` // env var name; default AWS_SECRET_ACCESS_KEY
	SourceToken     string `json:"source_token,omitempty"`      // env var name; default AWS_SESSION_TOKEN
}

// Provider implements credentials.Provider.
type Provider struct {
	// HTTPClient is exposed for tests to point at an httptest.Server.
	// Zero value → http.DefaultClient.
	HTTPClient *http.Client
	// Now overrides the clock for deterministic signature tests.
	Now func() time.Time
}

// Name returns the plugin name.
func (Provider) Name() string { return ProviderName }

// NewCredential validates spec and returns a Credential that will
// mint fresh STS creds on every Materialize call.
func (p Provider) NewCredential(_ context.Context, cs credentials.CredentialSpec) (credentials.Credential, error) {
	var s Spec
	if len(cs.Spec) > 0 {
		if err := json.Unmarshal(cs.Spec, &s); err != nil {
			return nil, fmt.Errorf("sts provider: decoding spec: %w", err)
		}
	}
	if s.RoleARN == "" {
		return nil, fmt.Errorf("sts provider: role_arn is required")
	}
	if s.Duration == "" {
		s.Duration = "15m"
	}
	if s.Region == "" {
		s.Region = "us-east-1"
	}
	if s.SessionName == "" {
		s.SessionName = "forge-jit"
	}
	if s.SourceAccessKey == "" {
		s.SourceAccessKey = "AWS_ACCESS_KEY_ID"
	}
	if s.SourceSecretKey == "" {
		s.SourceSecretKey = "AWS_SECRET_ACCESS_KEY"
	}
	if s.SourceToken == "" {
		s.SourceToken = "AWS_SESSION_TOKEN"
	}
	dur, err := time.ParseDuration(s.Duration)
	if err != nil {
		return nil, fmt.Errorf("sts provider: parsing duration %q: %w", s.Duration, err)
	}
	if dur < 900*time.Second || dur > 12*time.Hour {
		return nil, fmt.Errorf("sts provider: duration %s outside AWS bounds [15m, 12h]", dur)
	}
	return &Credential{
		spec:       s,
		duration:   dur,
		httpClient: p.HTTPClient,
		now:        p.Now,
	}, nil
}

// Credential is the materializer returned by Provider.NewCredential.
type Credential struct {
	spec       Spec
	duration   time.Duration
	httpClient *http.Client
	now        func() time.Time

	// cacheMu guards the fields below. Materialize is called from
	// multiple goroutines (one per tool invocation on a shared
	// injector) so cache access must be serialized.
	cacheMu     sync.Mutex
	cached      credentials.Materialization
	cachedUntil time.Time // wall-clock instant after which the cache is stale
}

// cacheSkew is the safety margin subtracted from the STS-issued
// expiration when deciding whether the cached credential is still
// safe to hand out. Reviewer @initializ-mk asked for caching
// within TTL; giving up 60s at the tail keeps the tool call from
// racing against expiration on a slow subprocess.
const cacheSkew = 60 * time.Second

// Kind returns the provider name for audit-event tagging.
func (*Credential) Kind() string { return ProviderName }

// stsResponse is the subset of the AssumeRole XML response we care
// about. STS returns:
//
//	<AssumeRoleResponse>
//	  <AssumeRoleResult>
//	    <Credentials>
//	      <AccessKeyId>...</AccessKeyId>
//	      <SecretAccessKey>...</SecretAccessKey>
//	      <SessionToken>...</SessionToken>
//	      <Expiration>2026-07-02T15:00:00Z</Expiration>
//	    </Credentials>
//	  </AssumeRoleResult>
//	</AssumeRoleResponse>
type stsResponse struct {
	XMLName xml.Name `xml:"AssumeRoleResponse"`
	Result  struct {
		Credentials struct {
			AccessKeyID     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			SessionToken    string `xml:"SessionToken"`
			Expiration      string `xml:"Expiration"`
		} `xml:"Credentials"`
	} `xml:"AssumeRoleResult"`
}

// stsError is STS's Query-API error shape.
type stsError struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

// Materialize returns short-lived AWS credentials as env vars.
//
// Caches per-Credential: since Provider ignores per-call `args` (no
// scope-down based on tool input), every call for a given spec yields
// an equivalent credential, so serving the same materialization until
// TTL-minus-skew expiration is safe and strictly better than re-issuing
// on every tool call. Reviewer @initializ-mk asked for this — pre-fix,
// an agent that ran `aws` N times made N AssumeRole calls, adding
// latency per exec and risking account-level STS throttling.
//
// The cache is per-Credential (per-spec) and read under a mutex so
// concurrent tool invocations from the same skill share.
//
// No revocation callback — STS creds expire on their own; the runner
// records TTL for audit.
func (c *Credential) Materialize(ctx context.Context, _ string, _ json.RawMessage) (credentials.Materialization, error) {
	// Fast path: reuse a cached materialization when it's still safely
	// within TTL.
	c.cacheMu.Lock()
	if !c.cachedUntil.IsZero() && c.nowFn().Before(c.cachedUntil) {
		mat := c.cached
		c.cacheMu.Unlock()
		return cloneMaterialization(mat), nil
	}
	c.cacheMu.Unlock()

	// Slow path: mint a fresh credential.
	accessKey := os.Getenv(c.spec.SourceAccessKey)
	secretKey := os.Getenv(c.spec.SourceSecretKey)
	sessionToken := os.Getenv(c.spec.SourceToken) // optional (present when running under a role)
	if accessKey == "" || secretKey == "" {
		return credentials.Materialization{}, fmt.Errorf(
			"sts provider: source creds missing (%s / %s unset)",
			c.spec.SourceAccessKey, c.spec.SourceSecretKey)
	}

	form := url.Values{}
	form.Set("Action", "AssumeRole")
	form.Set("Version", "2011-06-15")
	form.Set("RoleArn", c.spec.RoleARN)
	form.Set("RoleSessionName", c.spec.SessionName)
	form.Set("DurationSeconds", strconv.Itoa(int(c.duration.Seconds())))
	if c.spec.ExternalID != "" {
		form.Set("ExternalId", c.spec.ExternalID)
	}
	if c.spec.SessionPolicy != "" {
		form.Set("Policy", c.spec.SessionPolicy)
	}
	body := form.Encode()

	endpoint := c.spec.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://sts.%s.amazonaws.com/", c.spec.Region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return credentials.Materialization{}, fmt.Errorf("sts provider: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	c.signSigV4(req, []byte(body), sigV4Creds{
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		SessionToken:    sessionToken,
	})

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return credentials.Materialization{}, fmt.Errorf("sts provider: POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return credentials.Materialization{}, fmt.Errorf("sts provider: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e stsError
		_ = xml.Unmarshal(respBody, &e)
		if e.Error.Code != "" {
			return credentials.Materialization{}, fmt.Errorf(
				"sts provider: AssumeRole failed (%d %s): %s",
				resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return credentials.Materialization{}, fmt.Errorf(
			"sts provider: AssumeRole failed (%d): %s",
			resp.StatusCode, truncate(string(respBody), 200))
	}

	var out stsResponse
	if err := xml.Unmarshal(respBody, &out); err != nil {
		return credentials.Materialization{}, fmt.Errorf("sts provider: parsing response: %w", err)
	}
	creds := out.Result.Credentials
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" || creds.SessionToken == "" {
		return credentials.Materialization{}, fmt.Errorf("sts provider: empty credentials in response")
	}

	mat := credentials.Materialization{
		Env: map[string]string{
			"AWS_ACCESS_KEY_ID":     creds.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY": creds.SecretAccessKey,
			"AWS_SESSION_TOKEN":     creds.SessionToken,
		},
		TTL: credentials.Duration(c.duration.String()),
		// Revoke is nil — STS credentials expire on their own; there
		// is no API to invalidate them early. Operators wanting hard
		// revoke should switch to a Vault dynamic-secret provider.
	}

	// Populate the cache. Prefer the STS-issued Expiration when
	// parseable; fall back to now + duration. Either way subtract
	// cacheSkew so we hand out a stale credential.
	expireAt := c.nowFn().Add(c.duration)
	if creds.Expiration != "" {
		if t, err := time.Parse(time.RFC3339, creds.Expiration); err == nil {
			expireAt = t
		}
	}
	c.cacheMu.Lock()
	c.cached = mat
	c.cachedUntil = expireAt.Add(-cacheSkew)
	c.cacheMu.Unlock()

	return cloneMaterialization(mat), nil
}

// nowFn returns the injected clock or time.Now, so tests can advance
// the clock without touching the wall.
func (c *Credential) nowFn() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// cloneMaterialization deep-copies the env map so a caller can't
// mutate the cached entry via the returned Materialization.
func cloneMaterialization(m credentials.Materialization) credentials.Materialization {
	out := m
	if len(m.Env) > 0 {
		out.Env = make(map[string]string, len(m.Env))
		maps.Copy(out.Env, m.Env)
	}
	if len(m.Headers) > 0 {
		out.Headers = make(map[string]string, len(m.Headers))
		maps.Copy(out.Headers, m.Headers)
	}
	return out
}

// truncate cuts s to at most n runes with an ellipsis suffix.
// Slices on runes, not bytes — a byte slice at an arbitrary offset
// can split a multi-byte UTF-8 sequence and produce invalid UTF-8,
// which then goes into an error message. Reviewer @initializ-mk
// flagged this on #236.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// ---- SigV4 signer (STS-only, stripped from providers/sigv4_transport.go) ----

type sigV4Creds struct {
	AccessKeyID, SecretAccessKey, SessionToken string
}

// signSigV4 stamps Authorization + X-Amz-Date (+ X-Amz-Security-Token)
// headers on req. Body has already been read into payload. Service is
// hardcoded to "sts" since that's the only endpoint this package hits.
func (c *Credential) signSigV4(req *http.Request, payload []byte, creds sigV4Creds) {
	now := time.Now().UTC()
	if c.now != nil {
		now = c.now().UTC()
	}
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	payloadHash := sha256Hex(payload)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	canonicalHeaders, signedHeaders := canonicalHeaderSet(req.Header, host)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.Path),
		"", // STS AssumeRole uses form-encoded body; no query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/sts/aws4_request", dateStamp, c.spec.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(creds.SecretAccessKey, dateStamp, c.spec.Region, "sts")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
}

func canonicalHeaderSet(h http.Header, host string) (string, string) {
	pairs := make(map[string]string, len(h)+1)
	pairs["host"] = host
	for k, vals := range h {
		lower := strings.ToLower(k)
		if lower == "authorization" {
			continue
		}
		pairs[lower] = collapseHeaderValue(strings.Join(vals, ","))
	}
	names := make([]string, 0, len(pairs))
	for n := range pairs {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString(n)
		sb.WriteByte(':')
		sb.WriteString(pairs[n])
		sb.WriteByte('\n')
	}
	return sb.String(), strings.Join(names, ";")
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func collapseHeaderValue(v string) string {
	v = strings.TrimSpace(v)
	var sb strings.Builder
	prevSpace := false
	for i := range len(v) {
		ch := v[i]
		if ch == ' ' || ch == '\t' {
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		sb.WriteByte(ch)
		prevSpace = false
	}
	return sb.String()
}

func deriveSigningKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	k = hmacSHA256(k, []byte("aws4_request"))
	return k
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func init() {
	credentials.DefaultRegistry.Register(Provider{})
}
