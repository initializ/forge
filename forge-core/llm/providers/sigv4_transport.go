package providers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// AWS SigV4 outbound signing transport for LLM clients pointed at AWS
// Bedrock (issue #202 Phase 2). Hand-rolled rather than pulled in via
// aws-sdk-go-v2 to match the existing aws_sigv4 inbound-auth provider
// (forge-core/auth/providers/aws_sigv4) — the SDK adds ~5 MB to the
// binary for one signing function, and the algorithm is small enough
// to fit in one file with no transitive deps.
//
// SigV4 spec: https://docs.aws.amazon.com/general/latest/gr/signature-version-4.html
// Bedrock endpoint shape: https://bedrock-runtime.<region>.amazonaws.com/model/<model-id>/invoke
//
// Scope: this signer covers what Bedrock needs — the `bedrock` service
// in a single region, body-bearing POST requests, header signing, no
// query-string signing (Bedrock never uses pre-signed URLs). Anything
// fancier — multi-region requests, query-string auth, chunked uploads
// — is out of scope and would be the moment to swap to aws-sdk-go-v2.

// SigV4Credentials carries the resolved AWS credentials the signer
// uses. Populated either explicitly by the caller or via
// SigV4CredentialsFromEnv. SessionToken is set when the underlying
// credentials are temporary (STS AssumeRole, IRSA, EC2 instance
// metadata) and adds the x-amz-security-token header to every request.
type SigV4Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// SigV4CredentialsFromEnv reads the standard AWS env vars. Returns
// (creds, true) when AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are
// both set; (zero, false) otherwise. AWS_SESSION_TOKEN is optional.
//
// Web identity / IRSA / EC2 instance metadata are NOT resolved here —
// those need STS round-trips and the inbound aws_sigv4 provider's
// hand-rolled STS client (forge-core/auth/providers/aws_sigv4/sts_client.go)
// would be the model to copy. Tracked as a Phase 2 follow-up; the
// majority of Bedrock deployments today set AWS_* env vars (or
// AWS_PROFILE that resolves to them) before launching the agent.
func SigV4CredentialsFromEnv() (SigV4Credentials, bool) {
	ak := os.Getenv("AWS_ACCESS_KEY_ID")
	sk := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if ak == "" || sk == "" {
		return SigV4Credentials{}, false
	}
	return SigV4Credentials{
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}, true
}

// SigV4Transport is an http.RoundTripper that signs each outbound
// request with AWS Signature V4 before forwarding to the underlying
// transport.
//
// Credentials are read once per request via the Credentials getter so
// the transport plays well with rotating creds (an external watcher
// can update a sync.Atomic-wrapped value the getter reads). Region and
// Service are static — they describe the AWS endpoint being signed
// for, not the caller's identity.
//
// Underlying is what we delegate the round trip to after signing —
// composes cleanly with the existing otelhttp + egress-enforcer
// transports because we mutate headers only.
type SigV4Transport struct {
	Underlying  http.RoundTripper
	Credentials func() (SigV4Credentials, error)
	Region      string
	Service     string

	// now is injected so tests can pin the X-Amz-Date header to a
	// fixed value. Production callers leave it nil; the signer falls
	// back to time.Now().UTC().
	now func() time.Time
}

// RoundTrip signs req with SigV4 and forwards it to the underlying
// transport. Clones the request before mutating headers (the
// http.RoundTripper contract) and reads the body fully into memory
// because SigV4 hashes the payload — there is no streaming-signature
// option in the bedrock service.
//
// Body limits: chunked-streaming + unsigned payload is supported by
// SigV4 but Bedrock doesn't accept it. Reading the body in full is
// the price of pointing at Bedrock; bound your request sizes
// upstream.
func (t *SigV4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	creds, err := t.Credentials()
	if err != nil {
		return nil, fmt.Errorf("sigv4: resolving credentials: %w", err)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("sigv4: missing AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY")
	}
	if t.Region == "" {
		return nil, fmt.Errorf("sigv4: empty region")
	}
	if t.Service == "" {
		return nil, fmt.Errorf("sigv4: empty service")
	}

	// Buffer the body once so we can hash it AND replay it on retry.
	// SigV4 needs the payload digest; net/http needs to read the body
	// when it sends. Avoiding the buffer entirely would require
	// chunked signing which Bedrock doesn't accept.
	var bodyBytes []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("sigv4: reading request body: %w", err)
		}
		_ = req.Body.Close()
		bodyBytes = b
	}

	signed := req.Clone(req.Context())
	signed.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	signed.ContentLength = int64(len(bodyBytes))

	t.sign(signed, bodyBytes, creds)

	underlying := t.Underlying
	if underlying == nil {
		underlying = http.DefaultTransport
	}
	return underlying.RoundTrip(signed)
}

// sign computes the SigV4 signature and stamps the Authorization +
// X-Amz-Date (+ X-Amz-Security-Token when applicable) headers on req.
// Body has already been read into payload.
func (t *SigV4Transport) sign(req *http.Request, payload []byte, creds SigV4Credentials) {
	now := time.Now().UTC()
	if t.now != nil {
		now = t.now().UTC()
	}
	amzDate := now.Format("20060102T150405Z") // ISO 8601 basic
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	// Host header MUST be in the signed-headers set per spec. Go's
	// http client populates req.Host from the URL on send, but
	// req.Header.Get("Host") returns empty by default — set it
	// explicitly so the canonical-headers computation includes the
	// right value.
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
		canonicalQuery(req.URL.RawQuery),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, t.Region, t.Service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(creds.SecretAccessKey, dateStamp, t.Region, t.Service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
}

// canonicalHeaderSet returns (canonical-headers block, signed-headers
// list). Per SigV4 spec: lowercase header names, sorted ascending,
// each followed by its value with internal whitespace collapsed.
// We always include the host header (required) plus every other
// header present on the request — including the x-amz-* set we just
// stamped.
func canonicalHeaderSet(h http.Header, host string) (string, string) {
	pairs := make(map[string]string, len(h)+1)
	pairs["host"] = host
	for k, vals := range h {
		lower := strings.ToLower(k)
		// SigV4 excludes Authorization (we'd be signing the
		// header we're about to write) and the Content-Length /
		// User-Agent / Expect headers per the spec's
		// "unsignable" set. The Bedrock service accepts a
		// smaller set of signed headers than the general spec,
		// but signing extras is safe.
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

// canonicalURI returns the path component encoded per SigV4 rules —
// each segment gets URI-encoded except for unreserved characters. For
// Bedrock the path looks like
// "/model/anthropic.claude-sonnet-4-20250514-v1%3A0/invoke" which is
// already encoded by net/url when the operator built the request, so
// we re-encode safely (idempotent for the unreserved set).
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	// Re-encode each segment to ensure conformity. Bedrock's URLs
	// use percent-encoded colons in the model id; preserve those.
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = uriEscape(seg, false)
	}
	return strings.Join(segments, "/")
}

// canonicalQuery sorts query parameters by key, then by value, per
// SigV4 spec. Empty raw query → empty string.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	pairs := strings.Split(raw, "&")
	encoded := make([]string, 0, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			encoded = append(encoded, uriEscape(p, true)+"=")
			continue
		}
		encoded = append(encoded, uriEscape(k, true)+"="+uriEscape(v, true))
	}
	sort.Strings(encoded)
	return strings.Join(encoded, "&")
}

// uriEscape percent-encodes everything outside the SigV4 unreserved
// set (`A-Z`, `a-z`, `0-9`, `-`, `_`, `.`, `~`). When encodeSlash is
// true, slashes are encoded too (query-component rules); when false,
// they pass through (path-component rules).
func uriEscape(s string, encodeSlash bool) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z',
			'a' <= c && c <= 'z',
			'0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			sb.WriteByte(c)
		case c == '/' && !encodeSlash:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

// collapseHeaderValue trims and collapses repeated internal whitespace
// per the SigV4 canonical-header rules.
func collapseHeaderValue(v string) string {
	v = strings.TrimSpace(v)
	var sb strings.Builder
	prevSpace := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == ' ' || c == '\t' {
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		sb.WriteByte(c)
		prevSpace = false
	}
	return sb.String()
}

// deriveSigningKey runs the SigV4 HMAC chain to produce the final
// signing key. Walks date → region → service → "aws4_request".
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
