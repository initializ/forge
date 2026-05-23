package azure_ad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// GraphClient calls Microsoft Graph /me/transitiveMemberOf to enrich
// group memberships when the JWT's groups claim overflows (AAD truncates
// groups when the user is in more than ~200 of them).
//
// Forge holds NO Graph credentials of its own — the caller's Bearer
// token is reflected to Graph, which authorizes the read against the
// user's delegated permission (GroupMember.Read.All).
type GraphClient struct {
	endpoint string // override-able for tests
	http     *http.Client
}

const graphBaseURL = "https://graph.microsoft.com/v1.0/me/transitiveMemberOf?$select=id&$top=100"

// NewGraphClient builds a client pointed at the real Graph endpoint.
func NewGraphClient(timeout time.Duration) *GraphClient {
	return &GraphClient{
		endpoint: graphBaseURL,
		http:     &http.Client{Timeout: timeout},
	}
}

// NewGraphClientWithEndpoint is a TEST-ONLY constructor for pointing at
// a fake Graph server.
func NewGraphClientWithEndpoint(endpoint string, timeout time.Duration) *GraphClient {
	return &GraphClient{
		endpoint: endpoint,
		http:     &http.Client{Timeout: timeout},
	}
}

// TransitiveMemberOf walks the paginated response and returns the full
// list of (transitive) group object IDs the caller belongs to. The
// authHeader is reflected verbatim — Forge does not authenticate to Graph
// independently.
//
// Error classification:
//
//	401 / 403   → auth.ErrTokenRejected (caller's token missing
//	              GroupMember.Read.All consent)
//	5xx / network → auth.ErrProviderUnavailable
//	@odata.nextLink pointing at a foreign host → error (never followed)
func (c *GraphClient) TransitiveMemberOf(ctx context.Context, _ string, authHeader string) ([]string, error) {
	if authHeader == "" {
		return nil, fmt.Errorf("%w: graph enrichment needs a forwardable Bearer", auth.ErrInvalidToken)
	}
	out := []string{}
	next := c.endpoint
	for next != "" {
		if err := ensureGraphHost(c.endpoint, next); err != nil {
			return nil, fmt.Errorf("%w: graph nextLink host: %v", auth.ErrProviderUnavailable, err)
		}
		page, nextURL, err := c.fetchPage(ctx, next, authHeader)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		next = nextURL
		if len(out) > 5000 {
			return nil, errors.New("graph response exceeds 5000 groups (likely misconfiguration)")
		}
	}
	return out, nil
}

func (c *GraphClient) fetchPage(ctx context.Context, u, authHeader string) (ids []string, next string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: graph fetch: %v", auth.ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap per page
	if err != nil {
		return nil, "", fmt.Errorf("%w: graph read: %v", auth.ErrProviderUnavailable, err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		// fall through to parse
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, "", fmt.Errorf("%w: graph %d (likely missing GroupMember.Read.All consent)", auth.ErrTokenRejected, resp.StatusCode)
	case resp.StatusCode >= 500:
		return nil, "", fmt.Errorf("%w: graph HTTP %d", auth.ErrProviderUnavailable, resp.StatusCode)
	default:
		return nil, "", fmt.Errorf("%w: graph HTTP %d", auth.ErrProviderUnavailable, resp.StatusCode)
	}

	var page struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
		NextLink string `json:"@odata.nextLink"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", fmt.Errorf("%w: graph parse: %v", auth.ErrProviderUnavailable, err)
	}
	ids = make([]string, 0, len(page.Value))
	for _, g := range page.Value {
		ids = append(ids, g.ID)
	}
	return ids, page.NextLink, nil
}

// ensureGraphHost rejects @odata.nextLink values that point at a foreign
// host. Real Graph paginates within graph.microsoft.com. For tests where
// the endpoint is httptest's 127.0.0.1, the test-mode endpoint host is
// what we compare against.
func ensureGraphHost(configured, candidate string) error {
	if candidate == "" {
		return nil
	}
	want, err := url.Parse(configured)
	if err != nil {
		return err
	}
	got, err := url.Parse(candidate)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want.Host, got.Host) {
		return fmt.Errorf("nextLink host %q does not match configured %q", got.Host, want.Host)
	}
	return nil
}
