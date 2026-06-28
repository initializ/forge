package providers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureRT records the request its RoundTrip was called with so the
// tests can inspect headers the signer stamped.
type captureRT struct {
	got *http.Request
	err error
}

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	c.got = req
	return &http.Response{
		StatusCode: 200,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// fixedClock makes the signer's time deterministic so the canonical
// request — and therefore the signature — is reproducible.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestSigV4Transport_StampsAuthorizationAndAmzDate is the core spec
// invariant: every request gets `Authorization: AWS4-HMAC-SHA256 …`
// + `X-Amz-Date`. We don't pin the exact signature byte string
// because that depends on URL/header canonicalization stability;
// instead we assert the headers exist with the right shape.
func TestSigV4Transport_StampsAuthorizationAndAmzDate(t *testing.T) {
	cap := &captureRT{}
	tr := &SigV4Transport{
		Underlying: cap,
		Region:     "us-east-1",
		Service:    "bedrock",
		Credentials: func() (SigV4Credentials, error) {
			return SigV4Credentials{
				AccessKeyID:     "AKIATEST",
				SecretAccessKey: "secret-test",
			}, nil
		},
		now: fixedClock(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)),
	}
	req, _ := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-sonnet-4-20250514-v1%3A0/invoke",
		bytes.NewReader([]byte(`{"hello":"world"}`)))

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got := cap.got
	if got == nil {
		t.Fatal("underlying never invoked")
	}
	if d := got.Header.Get("X-Amz-Date"); d != "20260628T120000Z" {
		t.Errorf("X-Amz-Date = %q, want 20260628T120000Z", d)
	}
	auth := got.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIATEST/20260628/us-east-1/bedrock/aws4_request") {
		t.Errorf("Authorization missing AWS4-HMAC-SHA256 + credential scope prefix: %q", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=") || !strings.Contains(auth, "Signature=") {
		t.Errorf("Authorization missing SignedHeaders / Signature: %q", auth)
	}
}

// TestSigV4Transport_StampsSecurityTokenWhenTemporary pins the
// behavior for temporary credentials (STS, IRSA, EC2 metadata): the
// X-Amz-Security-Token header MUST be present and reflect the session
// token. Without it Bedrock rejects every request with InvalidSecurity.
func TestSigV4Transport_StampsSecurityTokenWhenTemporary(t *testing.T) {
	cap := &captureRT{}
	tr := &SigV4Transport{
		Underlying: cap,
		Region:     "us-east-1",
		Service:    "bedrock",
		Credentials: func() (SigV4Credentials, error) {
			return SigV4Credentials{
				AccessKeyID:     "ASIATEST",
				SecretAccessKey: "secret",
				SessionToken:    "FwoGZXIv-test-session",
			}, nil
		},
		now: fixedClock(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)),
	}
	req, _ := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/x", bytes.NewReader([]byte("{}")))
	_, _ = tr.RoundTrip(req)
	if got := cap.got.Header.Get("X-Amz-Security-Token"); got != "FwoGZXIv-test-session" {
		t.Errorf("X-Amz-Security-Token = %q", got)
	}
}

// TestSigV4Transport_PreservesBodyAndContentLength asserts the body
// survives signing — we read it to hash it, but we re-attach a fresh
// reader that the underlying transport can read.
func TestSigV4Transport_PreservesBodyAndContentLength(t *testing.T) {
	cap := &captureRT{}
	tr := &SigV4Transport{
		Underlying: cap,
		Region:     "us-east-1",
		Service:    "bedrock",
		Credentials: func() (SigV4Credentials, error) {
			return SigV4Credentials{AccessKeyID: "AKIA", SecretAccessKey: "x"}, nil
		},
		now: fixedClock(time.Now()),
	}
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/x", bytes.NewReader(body))
	_, _ = tr.RoundTrip(req)
	if cap.got.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", cap.got.ContentLength, len(body))
	}
	got, _ := io.ReadAll(cap.got.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("body lost during signing: got %q want %q", got, body)
	}
}

// TestSigV4Transport_DoesNotMutateCallerRequest is the
// http.RoundTripper contract. SigV4 stamps headers + reads the body;
// neither should leak back into the caller's request after RoundTrip
// returns. Tests retries: a caller that re-issues the same req must
// not see leftover Authorization from a prior round.
func TestSigV4Transport_DoesNotMutateCallerRequest(t *testing.T) {
	cap := &captureRT{}
	tr := &SigV4Transport{
		Underlying: cap,
		Region:     "us-east-1",
		Service:    "bedrock",
		Credentials: func() (SigV4Credentials, error) {
			return SigV4Credentials{AccessKeyID: "AKIA", SecretAccessKey: "x"}, nil
		},
		now: fixedClock(time.Now()),
	}
	orig, _ := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/x", bytes.NewReader([]byte("{}")))
	_, _ = tr.RoundTrip(orig)

	if orig.Header.Get("Authorization") != "" {
		t.Errorf("caller's request mutated; Authorization leaked: %q",
			orig.Header.Get("Authorization"))
	}
	if orig.Header.Get("X-Amz-Date") != "" {
		t.Errorf("caller's request mutated; X-Amz-Date leaked: %q",
			orig.Header.Get("X-Amz-Date"))
	}
}

// TestSigV4Transport_PropagatesUnderlyingError confirms the wrapper
// is transparent on the failure path — network errors / connection
// refuseds come back unchanged so callers retry / log normally.
func TestSigV4Transport_PropagatesUnderlyingError(t *testing.T) {
	want := errors.New("simulated transport failure")
	tr := &SigV4Transport{
		Underlying: &captureRT{err: want},
		Region:     "us-east-1",
		Service:    "bedrock",
		Credentials: func() (SigV4Credentials, error) {
			return SigV4Credentials{AccessKeyID: "AKIA", SecretAccessKey: "x"}, nil
		},
		now: fixedClock(time.Now()),
	}
	req, _ := http.NewRequest(http.MethodPost,
		"https://example.invalid", bytes.NewReader([]byte("{}")))
	_, err := tr.RoundTrip(req)
	if !errors.Is(err, want) {
		t.Errorf("RoundTrip err = %v, want %v", err, want)
	}
}

// TestSigV4Transport_MissingCredentialsErrors pins the misconfig
// path: if AWS env vars aren't set the credential getter returns an
// error and SigV4Transport surfaces it; the underlying transport is
// never invoked. Prevents an unsigned request from sneaking out.
func TestSigV4Transport_MissingCredentialsErrors(t *testing.T) {
	cap := &captureRT{}
	tr := &SigV4Transport{
		Underlying: cap,
		Region:     "us-east-1",
		Service:    "bedrock",
		Credentials: func() (SigV4Credentials, error) {
			return SigV4Credentials{}, errors.New("creds missing")
		},
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/", nil)
	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatalf("expected error; got nil")
	}
	if cap.got != nil {
		t.Errorf("underlying invoked despite credential error — unsigned request would have leaked")
	}
}

// TestSigV4Transport_RequiresRegionAndService pins the second misconfig
// path. Either an empty Region or an empty Service is an error before
// any signing happens.
func TestSigV4Transport_RequiresRegionAndService(t *testing.T) {
	makeReq := func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
		return r
	}
	creds := func() (SigV4Credentials, error) {
		return SigV4Credentials{AccessKeyID: "k", SecretAccessKey: "s"}, nil
	}
	if _, err := (&SigV4Transport{Service: "bedrock", Credentials: creds}).RoundTrip(makeReq()); err == nil {
		t.Errorf("empty Region should error")
	}
	if _, err := (&SigV4Transport{Region: "us-east-1", Credentials: creds}).RoundTrip(makeReq()); err == nil {
		t.Errorf("empty Service should error")
	}
}

// TestSigV4Transport_CanonicalQueryOrdering pins the algorithm's
// sort-by-key-then-value semantics. Without correct ordering the
// signature is wrong and Bedrock returns SignatureDoesNotMatch. Use
// two parameters that would canonicalize to different orderings if
// the sort were wrong.
func TestSigV4Transport_CanonicalQueryOrdering(t *testing.T) {
	got1 := canonicalQuery("b=2&a=1")
	got2 := canonicalQuery("a=1&b=2")
	if got1 != got2 {
		t.Errorf("query ordering not normalized: %q vs %q", got1, got2)
	}
	if got1 != "a=1&b=2" {
		t.Errorf("canonicalQuery = %q, want a=1&b=2", got1)
	}
}

// TestSigV4Transport_EndToEndAgainstHTTPTestServer drives a real
// http.Client through the transport against a stubbed server. The
// server inspects the inbound Authorization header and confirms it
// follows the SigV4 shape — proving the transport composes with
// http.DefaultTransport as the underlying.
func TestSigV4Transport_EndToEndAgainstHTTPTestServer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(204)
	}))
	defer srv.Close()

	c := &http.Client{
		Transport: &SigV4Transport{
			Underlying: http.DefaultTransport,
			Region:     "us-east-1",
			Service:    "bedrock",
			Credentials: func() (SigV4Credentials, error) {
				return SigV4Credentials{AccessKeyID: "AKIA", SecretAccessKey: "s"}, nil
			},
			now: fixedClock(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)),
		},
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/x",
		bytes.NewReader([]byte("{}")))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	_ = resp.Body.Close()
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIA/20260628/us-east-1/bedrock/aws4_request") {
		t.Errorf("server received Authorization = %q", gotAuth)
	}
}

// TestSigV4CredentialsFromEnv_ParsesStandardVars covers the env
// reader. Sets the three documented env vars and asserts they roundtrip
// into the struct; clears them all and asserts the ok=false return so
// the caller falls through to the error path.
func TestSigV4CredentialsFromEnv_ParsesStandardVars(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA1234")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secretvalue")
	t.Setenv("AWS_SESSION_TOKEN", "FwoGZXIvtest")

	c, ok := SigV4CredentialsFromEnv()
	if !ok {
		t.Fatalf("expected ok=true with all three vars set")
	}
	if c.AccessKeyID != "AKIA1234" || c.SecretAccessKey != "secretvalue" || c.SessionToken != "FwoGZXIvtest" {
		t.Errorf("creds round-trip: got %+v", c)
	}

	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if _, ok := SigV4CredentialsFromEnv(); ok {
		t.Errorf("expected ok=false with missing required vars")
	}
}
