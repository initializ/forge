package aws_sigv4

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// STSClient invokes a pre-signed STS GetCallerIdentity URL produced by the
// caller's AWS SDK. Because the signature is in the URL's query parameters
// (not headers tied to a destination host), the request validates against
// STS no matter who relays it — that's the design property that lets
// Forge act as a verifier without holding any AWS secrets.
//
// ~80 LOC of hand-rolled HTTP + XML. No aws-sdk-go-v2 dependency
// (decision §9.1).
type STSClient struct {
	http *http.Client
}

// NewSTSClient builds a client that GETs the URL the caller pre-signed.
// `region` is informational here (the URL itself carries the region in
// its credential scope and host); we keep the arg for symmetry with the
// pre-rewrite API and for future per-region tuning.
//
// CheckRedirect is pinned to ErrUseLastResponse. STS never legitimately
// issues 3xx; the parser-side host gate (sigv4_parser.go's expectedHost)
// only validates the FIRST hop. If we let Go's default policy auto-follow
// a 302, an attacker (MITM with a valid cert, TLS-inspecting corporate
// proxy, DNS hijack) could redirect us to a foreign URL whose body becomes
// the parsed STS XML — and that XML controls Identity.UserID/OrgID/Arn.
// Refuse redirects outright so the same-host guard actually holds.
func NewSTSClient(_ /* region */ string, _ /* legacyOverrideUnused */ string, timeout time.Duration) *STSClient {
	return &STSClient{
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// CallerIdentity is the parsed STS response — the canonical identifiers
// Forge stamps into auth.Identity.
type CallerIdentity struct {
	UserID  string // e.g. "AROAJ...:session-name"
	Arn     string // e.g. "arn:aws:sts::123:assumed-role/ci-deploy/session"
	Account string // e.g. "123456789012"
}

// GetCallerIdentity GETs the pre-signed URL and parses the XML response.
//
// Error classification:
//
//	200 OK         → CallerIdentity, nil
//	4xx            → auth.ErrTokenRejected (caller's signature didn't validate;
//	                 most often "SignatureDoesNotMatch", "ExpiredToken", or
//	                 "InvalidClientTokenId")
//	5xx / network  → auth.ErrProviderUnavailable (review #6 contract)
//	parse failure  → auth.ErrProviderUnavailable (unexpected response shape)
func (c *STSClient) GetCallerIdentity(ctx context.Context, presignedURL string) (*CallerIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, presignedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build STS request: %v", auth.ErrProviderUnavailable, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: STS RPC: %v", auth.ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Real GetCallerIdentity responses are ~1 KiB; cap at 64 KiB.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("%w: read STS body: %v", auth.ErrProviderUnavailable, err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return parseGetCallerIdentityResponse(raw)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, fmt.Errorf("%w: STS rejected signature: %s", auth.ErrTokenRejected, summarize(raw))
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: STS HTTP %d", auth.ErrProviderUnavailable, resp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: STS unexpected status %d", auth.ErrProviderUnavailable, resp.StatusCode)
	}
}

// parseGetCallerIdentityResponse extracts the three canonical fields.
// STS always emits all three; absence is treated as a malformed reply.
func parseGetCallerIdentityResponse(raw []byte) (*CallerIdentity, error) {
	var resp struct {
		XMLName xml.Name `xml:"GetCallerIdentityResponse"`
		Result  struct {
			UserID  string `xml:"UserId"`
			Account string `xml:"Account"`
			Arn     string `xml:"Arn"`
		} `xml:"GetCallerIdentityResult"`
	}
	if err := xml.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: parse STS XML: %v", auth.ErrProviderUnavailable, err)
	}
	if resp.Result.Arn == "" || resp.Result.Account == "" || resp.Result.UserID == "" {
		return nil, fmt.Errorf("%w: STS XML missing required fields", auth.ErrProviderUnavailable)
	}
	return &CallerIdentity{
		UserID:  resp.Result.UserID,
		Arn:     resp.Result.Arn,
		Account: resp.Result.Account,
	}, nil
}

// summarize returns a short, single-line, log-safe rendering of an STS
// error body. Caps at 200 chars and strips newlines so STS error text
// (which can echo the caller's headers in some shapes) never propagates
// to logs verbatim.
func summarize(raw []byte) string {
	s := string(raw)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.ReplaceAll(s, "\n", " ")
}
