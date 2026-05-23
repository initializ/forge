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

const happySTSXML = `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetCallerIdentityResult>
    <UserId>AROAJ123:session</UserId>
    <Account>123456789012</Account>
    <Arn>arn:aws:sts::123456789012:assumed-role/ci-deploy/session</Arn>
  </GetCallerIdentityResult>
  <ResponseMetadata><RequestId>req-id</RequestId></ResponseMetadata>
</GetCallerIdentityResponse>`

func TestSTSClient_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, happySTSXML)
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	id, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{
		AuthHeader: "AWS4-HMAC-SHA256 Credential=AKIA",
		AmzDate:    "20260523T120000Z",
	})
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
	if id.Arn != "arn:aws:sts::123456789012:assumed-role/ci-deploy/session" {
		t.Errorf("Arn = %q", id.Arn)
	}
	if id.Account != "123456789012" {
		t.Errorf("Account = %q", id.Account)
	}
	if id.UserID != "AROAJ123:session" {
		t.Errorf("UserID = %q", id.UserID)
	}
}

func TestSTSClient_403_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<ErrorResponse><Error><Code>SignatureDoesNotMatch</Code></Error></ErrorResponse>")
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{AuthHeader: "AWS4"})
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestSTSClient_500_Unavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>")
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{AuthHeader: "AWS4"})
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_NetworkError_Unavailable(t *testing.T) {
	// Point at a closed listener so Do() returns a transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewSTSClient("us-east-1", url, 1*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{AuthHeader: "AWS4"})
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_AuthHeaderReflectedVerbatim(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, happySTSXML)
	}))
	defer srv.Close()

	wantAuth := "AWS4-HMAC-SHA256 Credential=AKIA.../20260523/us-east-1/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc123"
	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{
		AuthHeader:    wantAuth,
		AmzDate:       "20260523T120000Z",
		SecurityToken: "FwoGZX-test-session",
	})
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
	if captured != wantAuth {
		t.Errorf("STS did not receive caller's Authorization verbatim:\n  got:  %q\n  want: %q", captured, wantAuth)
	}
}

func TestSTSClient_SecurityTokenReflected(t *testing.T) {
	var capturedTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTok = r.Header.Get("X-Amz-Security-Token")
		_, _ = io.WriteString(w, happySTSXML)
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	_, _ = c.GetCallerIdentity(context.Background(), STSReflectArgs{
		AuthHeader:    "AWS4",
		AmzDate:       "20260523T120000Z",
		SecurityToken: "FwoGZX-token",
	})
	if capturedTok != "FwoGZX-token" {
		t.Errorf("X-Amz-Security-Token = %q, want FwoGZX-token", capturedTok)
	}
}

func TestSTSClient_BodyCap(t *testing.T) {
	// Serve 128 KiB; ensure the client doesn't OOM and the response
	// either parses cleanly (truncated at exactly a well-formed prefix
	// — unlikely) OR returns ErrProviderUnavailable from parse failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<GetCallerIdentityResponse>")
		_, _ = io.WriteString(w, strings.Repeat("A", 128<<10))
		_, _ = io.WriteString(w, "</GetCallerIdentityResponse>")
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{AuthHeader: "AWS4"})
	if err == nil {
		t.Fatal("expected error on oversized STS body")
	}
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_MissingFieldsRejected(t *testing.T) {
	// Response with empty Arn — must reject as unavailable (malformed,
	// not "rejected" — STS would never legitimately return blank fields).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<GetCallerIdentityResponse><GetCallerIdentityResult>
			<UserId>x</UserId><Account>123</Account><Arn></Arn>
		</GetCallerIdentityResult></GetCallerIdentityResponse>`)
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{AuthHeader: "AWS4"})
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_RegionEndpointFormat(t *testing.T) {
	// Sanity: NewSTSClient without an override builds the right URL.
	c := NewSTSClient("eu-west-1", "", time.Second)
	if c.endpoint != "https://sts.eu-west-1.amazonaws.com" {
		t.Errorf("endpoint = %q", c.endpoint)
	}
}

func TestSTSClient_RequestCount(t *testing.T) {
	// Pin the basic happy-path call counting for use by the provider-level
	// cache-hit test.
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, happySTSXML)
	}))
	defer srv.Close()
	c := NewSTSClient("us-east-1", srv.URL, 5*time.Second)
	for i := range 3 {
		_, err := c.GetCallerIdentity(context.Background(), STSReflectArgs{AuthHeader: "AWS4"})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("STS calls = %d, want 3", calls.Load())
	}
}
