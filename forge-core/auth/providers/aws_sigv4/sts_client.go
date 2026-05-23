package aws_sigv4

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// STSClient calls AWS STS GetCallerIdentity by reflecting the caller's
// Sigv4 signature. ~150 LOC of hand-rolled HTTP + XML — no aws-sdk-go-v2
// dependency (decision §9.1).
//
// The key security property: Forge NEVER possesses the caller's secret
// key. STS validates the signature on Forge's behalf and returns the
// canonical ARN / UserId / Account on success.
type STSClient struct {
	endpoint string
	http     *http.Client
}

// NewSTSClient builds a client for sts.<region>.amazonaws.com. The override
// argument is for tests only — production should leave it empty.
func NewSTSClient(region, override string, timeout time.Duration) *STSClient {
	ep := override
	if ep == "" {
		ep = fmt.Sprintf("https://sts.%s.amazonaws.com", region)
	}
	return &STSClient{
		endpoint: ep,
		http:     &http.Client{Timeout: timeout},
	}
}

// STSReflectArgs is the set of headers reflected verbatim from the caller's
// request to the STS call. SecurityToken is optional and only present when
// the caller is using temporary credentials (assumed roles, federation,
// IRSA, etc.).
type STSReflectArgs struct {
	AuthHeader    string
	AmzDate       string
	SecurityToken string
}

// CallerIdentity is the parsed STS response — the canonical identifiers
// Forge stamps into auth.Identity.
type CallerIdentity struct {
	UserID  string // e.g. "AROAJ...:session-name"
	Arn     string // e.g. "arn:aws:sts::123:assumed-role/ci-deploy/session"
	Account string // e.g. "123456789012"
}

// GetCallerIdentity reflects the caller's Sigv4 headers to STS and parses
// the response.
//
// Error classification:
//
//	200 OK         → CallerIdentity, nil
//	4xx            → auth.ErrTokenRejected (caller's signature didn't validate)
//	5xx / network  → auth.ErrProviderUnavailable (review #6 contract)
//	parse failure  → auth.ErrProviderUnavailable (unexpected response shape)
func (c *STSClient) GetCallerIdentity(ctx context.Context, args STSReflectArgs) (*CallerIdentity, error) {
	body := url.Values{
		"Action":  {"GetCallerIdentity"},
		"Version": {"2011-06-15"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build STS request: %v", auth.ErrProviderUnavailable, err)
	}
	req.Header.Set("Authorization", args.AuthHeader)
	req.Header.Set("X-Amz-Date", args.AmzDate)
	if args.SecurityToken != "" {
		req.Header.Set("X-Amz-Security-Token", args.SecurityToken)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: STS RPC: %v", auth.ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Real GetCallerIdentity responses are ~1 KiB; cap at 64 KiB to bound
	// memory if STS ever returns something pathological.
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

// parseGetCallerIdentityResponse extracts the three canonical fields from
// STS's XML response. Rejects responses that are missing any of them — STS
// always sets all three, so absence implies a malformed reply.
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
// error body. Caps at 200 chars and strips newlines so the full error
// payload — which can include the caller's Authorization echo on certain
// SDK-shaped errors — never propagates to logs verbatim.
func summarize(raw []byte) string {
	s := string(raw)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.ReplaceAll(s, "\n", " ")
}
