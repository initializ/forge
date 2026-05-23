package aws_sigv4

import (
	"errors"
	"strings"
)

// Sigv4Header is the parsed view of an AWS Sigv4 Authorization header.
type Sigv4Header struct {
	AKID          string // e.g. "AKIAIOSFODNN7EXAMPLE"
	Date          string // YYYYMMDD scope date
	Region        string // e.g. "us-east-1"
	Service       string // e.g. "sts"
	SignedHeaders string // semicolon-separated list — must include "host"
	Signature     string // hex-encoded HMAC
}

// Parser parses Sigv4-shaped Authorization headers. Pure string work —
// no HTTP, no AWS SDK. Never panics on input.
type Parser struct{}

const sigv4Algorithm = "AWS4-HMAC-SHA256"

// Parse extracts the fields from a Sigv4 Authorization header. Real-world
// signers vary in whitespace around the comma separators; we trim tolerantly
// rather than insist on exact bytes.
//
// Returns a clear error on malformed input — callers should map parse
// errors to auth.ErrInvalidToken, not auth.ErrTokenNotForMe (the latter
// is reserved for "no Sigv4 prefix at all").
func (Parser) Parse(authHeader string) (*Sigv4Header, error) {
	if !strings.HasPrefix(authHeader, sigv4Algorithm+" ") {
		return nil, errors.New("missing AWS4-HMAC-SHA256 prefix")
	}
	rest := strings.TrimPrefix(authHeader, sigv4Algorithm+" ")

	parts := splitCommaTrim(rest)
	if len(parts) != 3 {
		return nil, errors.New("expected 3 comma-separated kv pairs")
	}

	out := &Sigv4Header{}
	for _, p := range parts {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, errors.New("malformed kv pair")
		}
		switch strings.TrimSpace(k) {
		case "Credential":
			if err := parseCredentialScope(v, out); err != nil {
				return nil, err
			}
		case "SignedHeaders":
			out.SignedHeaders = strings.TrimSpace(v)
		case "Signature":
			out.Signature = strings.TrimSpace(v)
		}
	}

	if out.AKID == "" || out.Date == "" || out.Region == "" || out.Service == "" ||
		out.SignedHeaders == "" || out.Signature == "" {
		return nil, errors.New("missing one or more required Sigv4 fields")
	}
	if !signedHeadersContainsHost(out.SignedHeaders) {
		return nil, errors.New("SignedHeaders must include host")
	}
	return out, nil
}

func parseCredentialScope(v string, out *Sigv4Header) error {
	// Credential = AKID/YYYYMMDD/region/service/aws4_request
	segs := strings.Split(strings.TrimSpace(v), "/")
	if len(segs) != 5 || segs[4] != "aws4_request" {
		return errors.New("malformed Credential scope")
	}
	out.AKID, out.Date, out.Region, out.Service = segs[0], segs[1], segs[2], segs[3]
	return nil
}

// signedHeadersContainsHost validates that the SignedHeaders list includes
// "host" as a discrete entry. Sub-string match would accept "ghosting" — we
// split on ";" so each entry stands on its own.
func signedHeadersContainsHost(s string) bool {
	for h := range strings.SplitSeq(s, ";") {
		if strings.EqualFold(strings.TrimSpace(h), "host") {
			return true
		}
	}
	return false
}

func splitCommaTrim(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
