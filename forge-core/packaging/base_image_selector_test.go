package packaging

import "testing"

func TestSelectBaseImage_Default(t *testing.T) {
	bi := SelectBaseImage(nil, "", false)
	if bi.Image != "debian:bookworm-slim" {
		t.Errorf("Image = %q, want debian:bookworm-slim", bi.Image)
	}
	if bi.IsAlpine {
		t.Error("should not be alpine")
	}
}

func TestSelectBaseImage_ExplicitOverride(t *testing.T) {
	bi := SelectBaseImage(nil, "ubuntu:24.04", false)
	if bi.Image != "ubuntu:24.04" {
		t.Errorf("Image = %q, want ubuntu:24.04", bi.Image)
	}
}

func TestSelectBaseImage_Alpine(t *testing.T) {
	bi := SelectBaseImage(nil, "", true)
	if bi.Image != "alpine:3.20" {
		t.Errorf("Image = %q, want alpine:3.20", bi.Image)
	}
	if !bi.IsAlpine {
		t.Error("should be alpine")
	}
}

func TestSelectBaseImage_AlpineBlockedByUbuntu(t *testing.T) {
	resolutions := []BinResolution{
		{Name: "playwright", RequiresUbuntu: true},
	}
	bi := SelectBaseImage(resolutions, "", true)
	if bi.Image != "debian:bookworm-slim" {
		t.Errorf("Image = %q, want debian:bookworm-slim (ubuntu required)", bi.Image)
	}
	if bi.IsAlpine {
		t.Error("should not be alpine when ubuntu is required")
	}
}

func TestSelectBaseImage_NoUbuntuRequired(t *testing.T) {
	resolutions := []BinResolution{
		{Name: "jq"},
		{Name: "curl"},
	}
	bi := SelectBaseImage(resolutions, "", true)
	if bi.Image != "alpine:3.20" {
		t.Errorf("Image = %q, want alpine:3.20", bi.Image)
	}
}
