package tools

import (
	"context"
	"testing"
)

func TestRelaxedLimits_Roundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if RelaxedLimits(ctx) {
		t.Fatal("plain context must not report relaxed limits")
	}
	if !RelaxedLimits(WithRelaxedLimits(ctx)) {
		t.Fatal("stamped context must report relaxed limits")
	}
	// Stamping is inherited by derived contexts.
	child, cancel := context.WithCancel(WithRelaxedLimits(ctx))
	defer cancel()
	if !RelaxedLimits(child) {
		t.Fatal("derived context lost the relaxed-limits stamp")
	}
}
