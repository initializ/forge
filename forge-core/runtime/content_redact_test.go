package runtime

import (
	"strings"
	"testing"
)

func TestRedactSecrets_KnownPatterns(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantNot string // a substring that MUST NOT appear in the output
	}{
		{"anthropic_key", "key=sk-ant-12345abcdef67890abcdefXYZ end", "sk-ant-12345abcdef67890abcdefXYZ"},
		{"openai_key", "auth: sk-1234567890abcdefghijABCDEF tail", "sk-1234567890abcdefghijABCDEF"},
		{"github_pat", "token ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa val", "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"github_oauth", "auth gho_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb x", "gho_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"github_server", "header ghs_ssssssssssssssssssssssssssssssssssss y", "ghs_ssssssssssssssssssssssssssssssssssss"},
		{"github_fine", "pat github_pat_aaaaaaaaaaaaaaaaaaaaaa1234 z", "github_pat_aaaaaaaaaaaaaaaaaaaaaa1234"},
		{"aws_access", "AKIAIOSFODNN7EXAMPLE production-leak", "AKIAIOSFODNN7EXAMPLE"},
		{"slack_bot", "xoxb-1234567890-abcdef-bot-token-here ok", "xoxb-1234567890-abcdef-bot-token-here"},
		{"slack_user", "xoxp-9876543210-abcdef-user-token-here !", "xoxp-9876543210-abcdef-user-token-here"},
		{"telegram_bot", "tg=123456789:AAEhBP9-Klm-this-is-a-very-long-tg-bot-token-here", "123456789:AAEhBP9-Klm-this-is-a-very-long-tg-bot-token-here"},
		{
			"private_key",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAvDdt2g\n-----END RSA PRIVATE KEY-----",
			"MIIEowIBAAKCAQEAvDdt2g",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactSecrets(tc.input)
			if strings.Contains(out, tc.wantNot) {
				t.Errorf("secret leaked into redacted output\n  input:  %q\n  output: %q", tc.input, out)
			}
			if !strings.Contains(out, RedactionMarker) {
				t.Errorf("expected redaction marker %q in output, got %q", RedactionMarker, out)
			}
		})
	}
}

func TestRedactSecrets_PreservesSurroundingText(t *testing.T) {
	in := "Please use the key sk-ant-abcdefghij1234567890XYZ for testing"
	out := RedactSecrets(in)

	if !strings.HasPrefix(out, "Please use the key ") {
		t.Errorf("prefix lost; got %q", out)
	}
	if !strings.HasSuffix(out, " for testing") {
		t.Errorf("suffix lost; got %q", out)
	}
	if strings.Contains(out, "sk-ant-abcdefghij1234567890XYZ") {
		t.Errorf("secret survived redaction; got %q", out)
	}
}

func TestRedactSecrets_EmptyInput(t *testing.T) {
	if RedactSecrets("") != "" {
		t.Error("empty input must round-trip")
	}
}

func TestRedactSecrets_NoSecrets_NoOp(t *testing.T) {
	in := "What is the weather in Paris?"
	out := RedactSecrets(in)
	if out != in {
		t.Errorf("non-secret content was modified: %q -> %q", in, out)
	}
}

// TestPrepareSpanContent_RedactThenTruncate pins the ordering invariant:
// redact runs first, then byte-cap. If truncate ran first, a secret
// that straddled the cap boundary could survive in the truncated tail
// after the marker. The chosen order makes that impossible.
func TestPrepareSpanContent_RedactThenTruncate(t *testing.T) {
	// Build input that ends with a secret near the cap boundary.
	prefix := strings.Repeat("x", DefaultSpanContentCapBytes-30)
	secret := "AKIAIOSFODNN7EXAMPLE"
	in := prefix + " " + secret

	out := PrepareSpanContent(in, true, DefaultSpanContentCapBytes)
	if strings.Contains(out, secret) {
		t.Errorf("secret survived the redact-then-truncate pipeline near the cap boundary: %q", out)
	}
}

// TestPrepareSpanContent_RedactFalse_KeepsRawContent confirms the
// enterprise raw-capture path leaves the content untouched up to the
// byte cap. The cap still fires.
func TestPrepareSpanContent_RedactFalse_KeepsRawContent(t *testing.T) {
	in := "sk-ant-abcdefghij1234567890XYZ"
	out := PrepareSpanContent(in, false, DefaultSpanContentCapBytes)
	if out != in {
		t.Errorf("redact=false must not scrub; got %q want %q", out, in)
	}
}

// TestPrepareSpanContent_EmptyContent_FastPath confirms empty input
// short-circuits to empty output, so callers using a non-empty return
// to gate attribute stamping see "no opt-in" semantics for empty
// content.
func TestPrepareSpanContent_EmptyContent_FastPath(t *testing.T) {
	if got := PrepareSpanContent("", true, 100); got != "" {
		t.Errorf("empty input must return empty; got %q", got)
	}
}

// TestPrepareSpanContent_MaxBytesZero_FallsBackToDefault checks the
// caller-friendly default fallback. Operators / tests passing 0 get
// the package default rather than "no cap" (which would defeat the
// backend-attr-limit motivation).
func TestPrepareSpanContent_MaxBytesZero_FallsBackToDefault(t *testing.T) {
	// 5 KiB of content with 0 cap → truncated to the 4 KiB default.
	in := strings.Repeat("a", 5<<10)
	out := PrepareSpanContent(in, false, 0)
	if len(out) > DefaultSpanContentCapBytes+64 {
		t.Errorf("maxBytes=0 must default to DefaultSpanContentCapBytes; got len=%d", len(out))
	}
}

// TestPrepareSpanContent_TruncationMarkerMatchesAuditPipeline is the
// cross-pipeline parity check the issue called out by name. The
// marker shape on span content MUST be byte-identical to what the
// audit payload-capture path produces for the same input, so an
// operator grepping for "[truncated:" across both sinks sees aligned
// output.
func TestPrepareSpanContent_TruncationMarkerMatchesAuditPipeline(t *testing.T) {
	in := strings.Repeat("z", DefaultSpanContentCapBytes*2)

	spanOut := PrepareSpanContent(in, false, DefaultSpanContentCapBytes)
	auditOut := TruncateForAudit(in, DefaultSpanContentCapBytes)

	if spanOut != auditOut {
		t.Errorf("span and audit truncation outputs diverged for the same input:\n  span:  %q\n  audit: %q",
			spanOut, auditOut)
	}
	if !strings.Contains(spanOut, "[truncated:") {
		t.Errorf("expected truncation marker in span output; got %q", spanOut)
	}
}
