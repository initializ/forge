package aws_sigv4

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// makeToken builds a forge-aws-v1 token from a complete URL. Helper for
// tests — production tokens come from the AWS SDK's Presign().
func makeToken(rawURL string) string {
	return TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(rawURL))
}

const validPresignedURL = "https://sts.us-east-1.amazonaws.com/" +
	"?Action=GetCallerIdentity" +
	"&Version=2011-06-15" +
	"&X-Amz-Algorithm=AWS4-HMAC-SHA256" +
	"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20260524%2Fus-east-1%2Fsts%2Faws4_request" +
	"&X-Amz-Date=20260524T010000Z" +
	"&X-Amz-Expires=900" +
	"&X-Amz-SignedHeaders=host" +
	"&X-Amz-Signature=abcd1234"

const validHost = "sts.us-east-1.amazonaws.com"

func TestParseToken_HappyPath(t *testing.T) {
	tok := makeToken(validPresignedURL)
	parsed, err := ParseToken(tok, validHost, true)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if parsed.AKID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("AKID = %q", parsed.AKID)
	}
	if parsed.Date != "20260524" {
		t.Errorf("Date = %q", parsed.Date)
	}
	if parsed.Region != "us-east-1" {
		t.Errorf("Region = %q", parsed.Region)
	}
	if parsed.URL == nil || parsed.URL.Host != validHost {
		t.Errorf("URL host = %v", parsed.URL)
	}
}

func TestParseToken_MissingPrefix_NotForMe(t *testing.T) {
	cases := []string{
		"",
		"Bearer foo",
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.sig", // JWT-shaped
		"forge-aws-v0.something",                   // wrong version prefix
		"AWS4-HMAC-SHA256 Credential=AKIA...",      // old format
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := ParseToken(in, validHost, true)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "missing forge-aws-v1 prefix") {
				t.Errorf("err = %v, want missing-prefix error (so caller maps to ErrTokenNotForMe)", err)
			}
		})
	}
}

func TestParseToken_EmptyPayload(t *testing.T) {
	_, err := ParseToken(TokenPrefix, validHost, true)
	if err == nil {
		t.Fatal("expected error on empty payload")
	}
}

func TestParseToken_RejectsUserinfo(t *testing.T) {
	// Review M1: net/url parses "https://user:pass@sts.us-east-1.amazonaws.com"
	// into u.User != nil and u.Host == "sts.us-east-1.amazonaws.com" — the
	// host check alone passes. We must reject userinfo explicitly, otherwise
	// http.Client.Do would synthesize Authorization: Basic <b64> and ship
	// attacker bytes to STS.
	hostile := strings.Replace(
		validPresignedURL,
		"https://sts.us-east-1.amazonaws.com/",
		"https://attacker:secret@sts.us-east-1.amazonaws.com/",
		1,
	)
	_, err := ParseToken(makeToken(hostile), validHost, true)
	if err == nil {
		t.Fatal("expected error on URL with userinfo")
	}
	if !strings.Contains(err.Error(), "userinfo") {
		t.Errorf("err should mention userinfo; got %v", err)
	}
}

func TestParseToken_RejectsForeignHost(t *testing.T) {
	// SSRF guard — even if base64 decodes to a syntactically valid URL,
	// any non-STS host is rejected.
	hostile := "https://evil.example.com/" + strings.Replace(validPresignedURL, "https://sts.us-east-1.amazonaws.com/", "", 1)
	_, err := ParseToken(makeToken(hostile), validHost, true)
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("err = %v, want SSRF-guard rejection", err)
	}
}

func TestParseToken_RejectsHTTPScheme_InProdMode(t *testing.T) {
	httpURL := strings.Replace(validPresignedURL, "https://", "http://", 1)
	_, err := ParseToken(makeToken(httpURL), validHost, true)
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Errorf("err = %v, want https-required rejection", err)
	}
}

func TestParseToken_AcceptsHTTP_InTestMode(t *testing.T) {
	httpURL := strings.Replace(validPresignedURL, "https://", "http://", 1)
	_, err := ParseToken(makeToken(httpURL), validHost, false)
	if err != nil {
		t.Errorf("test-mode http should be accepted, got %v", err)
	}
}

func TestParseToken_RejectsWrongAction(t *testing.T) {
	u := strings.Replace(validPresignedURL, "Action=GetCallerIdentity", "Action=ListUsers", 1)
	_, err := ParseToken(makeToken(u), validHost, true)
	if err == nil || !strings.Contains(err.Error(), "Action") {
		t.Errorf("err = %v, want Action-mismatch rejection", err)
	}
}

func TestParseToken_RejectsMissingSignature(t *testing.T) {
	u := strings.Replace(validPresignedURL, "&X-Amz-Signature=abcd1234", "", 1)
	_, err := ParseToken(makeToken(u), validHost, true)
	if err == nil || !strings.Contains(err.Error(), "X-Amz-Signature") {
		t.Errorf("err = %v, want missing-signature rejection", err)
	}
}

func TestParseToken_RejectsWrongAlgorithm(t *testing.T) {
	u := strings.Replace(validPresignedURL, "X-Amz-Algorithm=AWS4-HMAC-SHA256", "X-Amz-Algorithm=AWS3-MD5", 1)
	_, err := ParseToken(makeToken(u), validHost, true)
	if err == nil || !strings.Contains(err.Error(), "X-Amz-Algorithm") {
		t.Errorf("err = %v, want algorithm-mismatch rejection", err)
	}
}

func TestParseToken_RejectsBadBase64(t *testing.T) {
	_, err := ParseToken(TokenPrefix+"!!!not-base64!!!", validHost, true)
	if err == nil {
		t.Fatal("expected error on malformed base64")
	}
}

func TestParseCredentialScope_HappyPath(t *testing.T) {
	akid, date, region, err := parseCredentialScope("AKIA123/20260524/us-east-1/sts/aws4_request")
	if err != nil {
		t.Fatalf("parseCredentialScope: %v", err)
	}
	if akid != "AKIA123" || date != "20260524" || region != "us-east-1" {
		t.Errorf("got %q/%q/%q", akid, date, region)
	}
}

func TestParseCredentialScope_Malformed(t *testing.T) {
	cases := map[string]string{
		"empty":              "",
		"too few segments":   "AKIA/20260524/us-east-1/sts",
		"too many segments":  "AKIA/20260524/us-east-1/sts/aws4_request/extra",
		"wrong service":      "AKIA/20260524/us-east-1/s3/aws4_request",
		"wrong tail":         "AKIA/20260524/us-east-1/sts/aws3_request",
		"empty AKID segment": "/20260524/us-east-1/sts/aws4_request",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseCredentialScope(in); err == nil {
				t.Errorf("expected error for %q", in)
			}
		})
	}
}

// FuzzParseToken — pure decoder must never panic, regardless of input.
func FuzzParseToken(f *testing.F) {
	f.Add(makeToken(validPresignedURL))
	f.Add("")
	f.Add(TokenPrefix)
	f.Add(strings.Repeat(TokenPrefix, 10))
	f.Add(TokenPrefix + "AAAA====")
	f.Fuzz(func(_ *testing.T, in string) {
		_, _ = ParseToken(in, validHost, true)
	})
}

// assert we map missing-prefix to a stable error message because the
// caller (provider.go) string-matches it to convert to ErrTokenNotForMe.
func TestParseToken_MissingPrefixErrorMessageIsStable(t *testing.T) {
	_, err := ParseToken("garbage", validHost, true)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, err) || err.Error() != "missing forge-aws-v1 prefix" {
		t.Errorf("err.Error() = %q, want exact string", err.Error())
	}
}
