package azure_ad

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

func TestGraph_HappyPath_Paginated(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	var graphURL string

	mux.HandleFunc("/page1", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"value":[{"id":"g1"},{"id":"g2"}],"@odata.nextLink":"`+graphURL+`/page2"}`)
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"value":[{"id":"g3"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	graphURL = srv.URL

	c := NewGraphClientWithEndpoint(srv.URL+"/page1", 5*time.Second)
	out, err := c.TransitiveMemberOf(context.Background(), "user-1", "Bearer token")
	if err != nil {
		t.Fatalf("TransitiveMemberOf: %v", err)
	}
	if len(out) != 3 || out[0] != "g1" || out[2] != "g3" {
		t.Errorf("groups = %v", out)
	}
	if calls != 2 {
		t.Errorf("pages fetched = %d, want 2", calls)
	}
}

func TestGraph_401_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := NewGraphClientWithEndpoint(srv.URL, 5*time.Second)
	_, err := c.TransitiveMemberOf(context.Background(), "user-1", "Bearer token")
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestGraph_403_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := NewGraphClientWithEndpoint(srv.URL, 5*time.Second)
	_, err := c.TransitiveMemberOf(context.Background(), "user-1", "Bearer token")
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestGraph_500_Unavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewGraphClientWithEndpoint(srv.URL, 5*time.Second)
	_, err := c.TransitiveMemberOf(context.Background(), "user-1", "Bearer token")
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestGraph_NoAuthHeader_Invalid(t *testing.T) {
	c := NewGraphClientWithEndpoint("http://does.not.matter", 5*time.Second)
	_, err := c.TransitiveMemberOf(context.Background(), "user-1", "")
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestGraph_AuthHeaderReflected(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"value":[]}`)
	}))
	defer srv.Close()
	c := NewGraphClientWithEndpoint(srv.URL, 5*time.Second)
	_, _ = c.TransitiveMemberOf(context.Background(), "user-1", "Bearer the-token")
	if captured != "Bearer the-token" {
		t.Errorf("Graph got Authorization = %q", captured)
	}
}

func TestGraph_DefensivePaginationCap(t *testing.T) {
	// Server keeps emitting @odata.nextLink to itself; expect cap to fire.
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, fmt.Sprintf(`{"value":[%s],"@odata.nextLink":"%s"}`, manyIDs(100), srvURL))
	}))
	defer srv.Close()
	srvURL = srv.URL
	c := NewGraphClientWithEndpoint(srv.URL, 5*time.Second)
	_, err := c.TransitiveMemberOf(context.Background(), "user-1", "Bearer x")
	if err == nil {
		t.Fatal("expected defensive cap error")
	}
}

func TestGraphClient_DoesNotFollowRedirects(t *testing.T) {
	// Review B2: ensureGraphHost only validates @odata.nextLink (the
	// application-layer paginator). HTTP 301/302/307 from Graph were
	// being auto-followed by Go's default policy, bypassing the host
	// guard. An attacker (MITM, TLS-inspecting proxy, DNS hijack)
	// returning a 302 to a foreign URL would have the response body
	// JSON-unmarshalled as `value: [{id: ...}]` — those IDs become
	// Identity.Groups, which the future Phase 4 authz layer will
	// trust. Pin CheckRedirect = ErrUseLastResponse.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Location", "https://attacker.example.com/value")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := NewGraphClientWithEndpoint(srv.URL, 5*time.Second)
	_, err := c.TransitiveMemberOf(context.Background(), "user-1", "Bearer token")
	if err == nil {
		t.Fatal("expected error on 302; client must not follow")
	}
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
	if hits != 1 {
		t.Errorf("graph hit %d times, want 1 (redirect was followed)", hits)
	}
}

func TestEnsureGraphHost_RejectsForeignHost(t *testing.T) {
	err := ensureGraphHost("graph.microsoft.com", "https", "https://evil.example.com/me/next")
	if err == nil {
		t.Fatal("expected error on foreign host")
	}
}

func TestEnsureGraphHost_AcceptsSameHost(t *testing.T) {
	err := ensureGraphHost(
		"graph.microsoft.com", "https",
		"https://graph.microsoft.com/v1.0/me/transitiveMemberOf?$skiptoken=abc",
	)
	if err != nil {
		t.Errorf("same-host nextLink rejected: %v", err)
	}
}

func TestEnsureGraphHost_EmptyOK(t *testing.T) {
	if err := ensureGraphHost("graph.microsoft.com", "https", ""); err != nil {
		t.Errorf("empty nextLink should be ok, got %v", err)
	}
}

func TestEnsureGraphHost_RejectsSchemeDowngrade(t *testing.T) {
	// Review B1: http://-on-same-host MUST be rejected. Go's http.Client
	// keeps Authorization across same-host redirects, so a downgrade
	// would leak the caller's Bearer in plaintext to anyone with a
	// network position to MITM the response.
	err := ensureGraphHost(
		"graph.microsoft.com", "https",
		"http://graph.microsoft.com/v1.0/me/transitiveMemberOf?$skiptoken=abc",
	)
	if err == nil {
		t.Fatal("expected error on http downgrade")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Errorf("err should mention scheme; got %v", err)
	}
}

func TestEnsureGraphHost_TestModeHTTPOK(t *testing.T) {
	// When the configured endpoint itself is http:// (httptest servers
	// in unit/integration tests), http://-same-host nextLinks must
	// still validate.
	err := ensureGraphHost(
		"127.0.0.1:54321", "http",
		"http://127.0.0.1:54321/page2",
	)
	if err != nil {
		t.Errorf("test-mode http nextLink should be accepted, got %v", err)
	}
}

// manyIDs returns a JSON snippet for `count` entries — used for the
// pagination cap test.
func manyIDs(count int) string {
	parts := make([]string, count)
	for i := range count {
		parts[i] = fmt.Sprintf(`{"id":"g-%d"}`, i)
	}
	return strings.Join(parts, ",")
}
