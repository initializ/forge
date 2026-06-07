package server

import (
	"context"
	"net/http"
	"testing"
)

func TestResponseHeaderStage_NoStageReturnsNil(t *testing.T) {
	if got := ResponseHeaderStageFromContext(context.Background()); got != nil {
		t.Errorf("nil-stage context should return nil; got %v", got)
	}
}

func TestResponseHeaderStage_AttachAndRead(t *testing.T) {
	ctx := WithResponseHeaderStage(context.Background())
	stage := ResponseHeaderStageFromContext(ctx)
	if stage == nil {
		t.Fatal("expected stage to be non-nil after WithResponseHeaderStage")
	}
	stage.Set("X-Forge-Tokens-In", "120")
	stage.Set("X-Forge-Tokens-Out", "45")

	// Same lookup should see the same backing map (no copy on read).
	again := ResponseHeaderStageFromContext(ctx)
	if again.Get("X-Forge-Tokens-In") != "120" {
		t.Errorf("stage values must persist across lookups; got %q", again.Get("X-Forge-Tokens-In"))
	}
}

func TestDrainResponseHeaderStage_CopiesOntoDestination(t *testing.T) {
	ctx := WithResponseHeaderStage(context.Background())
	stage := ResponseHeaderStageFromContext(ctx)
	stage.Set("X-Forge-Tokens-In", "120")
	stage.Set("X-Forge-Duration-Ms", "250")

	dst := http.Header{}
	dst.Set("Pre-existing", "intact")
	DrainResponseHeaderStage(ctx, dst)

	if dst.Get("X-Forge-Tokens-In") != "120" {
		t.Errorf("drain didn't copy X-Forge-Tokens-In; got %q", dst.Get("X-Forge-Tokens-In"))
	}
	if dst.Get("X-Forge-Duration-Ms") != "250" {
		t.Errorf("drain didn't copy X-Forge-Duration-Ms; got %q", dst.Get("X-Forge-Duration-Ms"))
	}
	if dst.Get("Pre-existing") != "intact" {
		t.Error("drain should not clobber pre-existing destination headers")
	}
}

func TestDrainResponseHeaderStage_NoStageIsNoop(t *testing.T) {
	dst := http.Header{}
	dst.Set("Pre-existing", "intact")
	DrainResponseHeaderStage(context.Background(), dst) // no panic; no-op
	if dst.Get("Pre-existing") != "intact" {
		t.Error("no-stage drain should not alter destination")
	}
	if len(dst) != 1 {
		t.Errorf("no-stage drain should not add headers; got %d entries", len(dst))
	}
}

func TestDrainResponseHeaderStage_EmptyStageIsNoop(t *testing.T) {
	ctx := WithResponseHeaderStage(context.Background())
	dst := http.Header{}
	DrainResponseHeaderStage(ctx, dst)
	if len(dst) != 0 {
		t.Errorf("empty-stage drain should produce empty destination; got %v", dst)
	}
}
