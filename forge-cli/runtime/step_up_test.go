package runtime

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/security/stepup"
)

// TestWriteStepUpChallengeOnError_HappyPath confirms the RFC 9470
// challenge format for a step-up-required error. The `Bearer error=
// "step_up_required", acr_values="<acr>"` header is what triggers
// the caller's SDK to re-authenticate at the higher assurance
// level.
func TestWriteStepUpChallengeOnError_HappyPath(t *testing.T) {
	re := &stepup.RequiredError{
		Tool:         "cli_execute",
		RequiredAcr:  "acr:mfa",
		PresentedAcr: "",
		Reason:       "no acr claim presented",
	}
	rec := httptest.NewRecorder()
	if !WriteStepUpChallengeOnError(rec, re) {
		t.Fatal("WriteStepUpChallengeOnError returned false on a step-up error")
	}
	if rec.Code != 401 {
		t.Errorf("status: got %d want 401", rec.Code)
	}
	authHdr := rec.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(authHdr, "Bearer ") {
		t.Errorf("WWW-Authenticate should start with Bearer: %q", authHdr)
	}
	if !strings.Contains(authHdr, `error="step_up_required"`) {
		t.Errorf("WWW-Authenticate missing error param: %q", authHdr)
	}
	if !strings.Contains(authHdr, `acr_values="acr:mfa"`) {
		t.Errorf("WWW-Authenticate missing acr_values param: %q", authHdr)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error":"step_up_required"`) {
		t.Errorf("body missing error field: %s", body)
	}
	if !strings.Contains(body, `"required_acr":"acr:mfa"`) {
		t.Errorf("body missing required_acr: %s", body)
	}
	if !strings.Contains(body, `"tool":"cli_execute"`) {
		t.Errorf("body missing tool: %s", body)
	}
}

// TestWriteStepUpChallengeOnError_UnwrapsWrappedError — the runner
// wraps step-up errors in "cli_execute: minting JIT credentials: %w"
// style messages. The writer must errors.As-unwrap them to find the
// step-up marker.
func TestWriteStepUpChallengeOnError_UnwrapsWrappedError(t *testing.T) {
	inner := &stepup.RequiredError{
		Tool:        "cli_execute",
		RequiredAcr: "acr:mfa",
		Reason:      "wrapped",
	}
	wrapped := &wrappedErr{inner: inner, msg: "tool exec: step_up_required"}
	rec := httptest.NewRecorder()
	if !WriteStepUpChallengeOnError(rec, wrapped) {
		t.Fatal("failed to unwrap step-up error")
	}
	if rec.Code != 401 {
		t.Errorf("status: got %d want 401", rec.Code)
	}
}

// TestWriteStepUpChallengeOnError_IgnoresOtherErrors — the writer
// must return false and touch nothing when the error isn't a
// step-up error; the caller's default error path takes over.
func TestWriteStepUpChallengeOnError_IgnoresOtherErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	if WriteStepUpChallengeOnError(rec, errors.New("unrelated error")) {
		t.Error("returned true on non-step-up error")
	}
	if rec.Code != 200 { // httptest default when WriteHeader not called
		t.Errorf("should not have written a response: code=%d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("should not have set WWW-Authenticate: %q", rec.Header().Get("WWW-Authenticate"))
	}
}

// TestWriteStepUpChallengeOnError_NilError — defensive: nil error
// is not a step-up error.
func TestWriteStepUpChallengeOnError_NilError(t *testing.T) {
	rec := httptest.NewRecorder()
	if WriteStepUpChallengeOnError(rec, nil) {
		t.Error("returned true on nil error")
	}
}

// wrappedErr is a small helper implementing errors.Unwrap so the
// AsRequiredError path is exercised.
type wrappedErr struct {
	inner error
	msg   string
}

func (w *wrappedErr) Error() string { return w.msg }
func (w *wrappedErr) Unwrap() error { return w.inner }
