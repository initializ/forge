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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("STS got method %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, happySTSXML)
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", "", 5*time.Second)
	id, err := c.GetCallerIdentity(context.Background(), srv.URL+"/?Action=GetCallerIdentity")
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

	c := NewSTSClient("us-east-1", "", 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), srv.URL)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestSTSClient_500_Unavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", "", 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), srv.URL)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_NetworkError_Unavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewSTSClient("us-east-1", "", 1*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), url)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_BodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<GetCallerIdentityResponse>")
		_, _ = io.WriteString(w, strings.Repeat("A", 128<<10))
		_, _ = io.WriteString(w, "</GetCallerIdentityResponse>")
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", "", 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error on oversized STS body")
	}
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_MissingFieldsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<GetCallerIdentityResponse><GetCallerIdentityResult>
			<UserId>x</UserId><Account>123</Account><Arn></Arn>
		</GetCallerIdentityResult></GetCallerIdentityResponse>`)
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", "", 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), srv.URL)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSTSClient_RequestCount(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, happySTSXML)
	}))
	defer srv.Close()
	c := NewSTSClient("us-east-1", "", 5*time.Second)
	for range 3 {
		if _, err := c.GetCallerIdentity(context.Background(), srv.URL); err != nil {
			t.Fatalf("call: %v", err)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("STS calls = %d, want 3", calls.Load())
	}
}

func TestSTSClient_DoesNotFollowRedirects(t *testing.T) {
	// Review B3: Go's default http.Client follows redirects up to 10
	// hops. The parser-side host gate only validates the first hop —
	// auto-following a 302 to attacker-controlled bytes would let
	// those bytes become the parsed STS XML and control the stamped
	// Identity. Pin: any 3xx is treated as STS-unavailable (we never
	// follow), so the same-host guard actually holds.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", "https://attacker.example.com/")
		w.WriteHeader(http.StatusFound) // 302
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", "", 5*time.Second)
	_, err := c.GetCallerIdentity(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error on 302; client must not follow")
	}
	// 3xx falls into the "unexpected status" arm → unavailable.
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
	if hits.Load() != 1 {
		t.Errorf("STS hit %d times, want exactly 1 (redirect was followed)", hits.Load())
	}
}

func TestSTSClient_PreservesURLQueryString(t *testing.T) {
	// The pre-signed URL carries the signature in query params; the
	// client MUST send those verbatim to STS or STS will reject.
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, happySTSXML)
	}))
	defer srv.Close()

	c := NewSTSClient("us-east-1", "", 5*time.Second)
	wantQuery := "Action=GetCallerIdentity&X-Amz-Signature=abc123"
	_, _ = c.GetCallerIdentity(context.Background(), srv.URL+"/?"+wantQuery)
	if capturedQuery != wantQuery {
		t.Errorf("STS received query %q, want %q", capturedQuery, wantQuery)
	}
}
