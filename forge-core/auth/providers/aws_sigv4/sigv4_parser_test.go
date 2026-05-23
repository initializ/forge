package aws_sigv4

import (
	"strings"
	"testing"
)

func TestParser_HappyPath(t *testing.T) {
	h := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20260523/us-east-1/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=ab12cd34"
	sig, err := (Parser{}).Parse(h)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if sig.AKID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("AKID = %q", sig.AKID)
	}
	if sig.Date != "20260523" {
		t.Errorf("Date = %q", sig.Date)
	}
	if sig.Region != "us-east-1" {
		t.Errorf("Region = %q", sig.Region)
	}
	if sig.Service != "sts" {
		t.Errorf("Service = %q", sig.Service)
	}
	if sig.SignedHeaders != "host;x-amz-date" {
		t.Errorf("SignedHeaders = %q", sig.SignedHeaders)
	}
	if sig.Signature != "ab12cd34" {
		t.Errorf("Signature = %q", sig.Signature)
	}
}

func TestParser_TolerantWhitespace(t *testing.T) {
	// Real-world Sigv4 signers vary in whitespace around the commas;
	// extra whitespace must still parse cleanly.
	h := "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts/aws4_request,SignedHeaders=host,Signature=abc"
	if _, err := (Parser{}).Parse(h); err != nil {
		t.Errorf("expected tolerant whitespace parse to succeed, got %v", err)
	}
}

func TestParser_MalformedInputs(t *testing.T) {
	cases := map[string]string{
		"empty":                      "",
		"bearer instead":             "Bearer foo",
		"prefix only":                "AWS4-HMAC-SHA256",
		"prefix and space":           "AWS4-HMAC-SHA256 ",
		"too few segments":           "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts, SignedHeaders=host, Signature=ab",
		"too many segments":          "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts/aws4_request/extra, SignedHeaders=host, Signature=ab",
		"missing SignedHeaders":      "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts/aws4_request, Signature=ab",
		"signed headers no host":     "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts/aws4_request, SignedHeaders=x-amz-date, Signature=ab",
		"scope tail wrong":           "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts/wrong, SignedHeaders=host, Signature=ab",
		"two parts instead of three": "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts/aws4_request, Signature=ab",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := (Parser{}).Parse(in); err == nil {
				t.Errorf("expected parse error for %q", in)
			}
		})
	}
}

func TestParser_SignedHeadersHostMatchExact(t *testing.T) {
	// "ghosting" must NOT count as containing "host" — we split on ";"
	// so each entry stands on its own.
	h := "AWS4-HMAC-SHA256 Credential=AKIA/20260523/us-east-1/sts/aws4_request, SignedHeaders=ghosting, Signature=ab"
	if _, err := (Parser{}).Parse(h); err == nil {
		t.Error("expected error: 'ghosting' should not satisfy host requirement")
	}
}

// FuzzParser ensures the parser never panics on arbitrary input. Run with
// `go test -fuzz=FuzzParser -fuzztime=30s ./forge-core/auth/providers/aws_sigv4/`.
func FuzzParser(f *testing.F) {
	f.Add("AWS4-HMAC-SHA256 Credential=A/B/C/D/aws4_request, SignedHeaders=host, Signature=x")
	f.Add("Bearer foo")
	f.Add("")
	f.Add(strings.Repeat("AWS4-HMAC-SHA256 ", 100))
	f.Fuzz(func(_ *testing.T, in string) {
		_, _ = (Parser{}).Parse(in)
	})
}
