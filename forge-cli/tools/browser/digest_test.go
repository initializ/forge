package browser

import (
	"fmt"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func sampleSnapshot() pageSnapshot {
	return pageSnapshot{
		URL:   "https://vendor.example/pricing",
		Title: "Pricing — Vendor",
		Gen:   7,
		Els: []elementInfo{
			{Index: 0, Tag: "a", Role: "link", Name: "Products", Href: "/products"},
			{Index: 1, Tag: "button", Role: "button", Name: "Contact sales"},
			{Index: 2, Tag: "input", Role: "input", Name: "Work email", InputType: "email"},
			{Index: 3, Tag: "input", Role: "input", Name: "Password", InputType: "password", Protected: true},
			{Index: 4, Tag: "input", Role: "input", Name: "Remember me", InputType: "checkbox", Checked: boolPtr(true)},
			{Index: 5, Tag: "select", Role: "select", Name: "Plan", Value: "Starter", Options: []string{"Starter", "Pro", "Enterprise"}},
		},
		TotalEls: 6,
		Text:     "Pro plan $49/user/month billed annually.",
		TextLen:  40,
	}
}

func TestBuildDigest_Format(t *testing.T) {
	d := buildDigest(sampleSnapshot())

	for _, want := range []string{
		"Page: Pricing — Vendor",
		"URL: https://vendor.example/pricing",
		"Generation: 7",
		`[0] link "Products" -> /products`,
		`[1] button "Contact sales"`,
		`[2] input(email) "Work email"`,
		`[3] input(password) "Password" ⚠ fill-protected`,
		`[4] input(checkbox) "Remember me" (checked)`,
		`[5] select "Plan" = "Starter" [Starter, Pro, Enterprise]`,
		"Pro plan $49/user/month",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("digest missing %q\n---\n%s", want, d)
		}
	}
	// All elements shown → no truncation notice.
	if strings.Contains(d, "showing") {
		t.Errorf("unexpected truncation notice in digest:\n%s", d)
	}
}

func TestBuildDigest_ElementTruncationNotice(t *testing.T) {
	snap := sampleSnapshot()
	snap.TotalEls = 143 // more elements exist than were captured
	d := buildDigest(snap)
	if !strings.Contains(d, "of 143 elements") {
		t.Errorf("digest missing element truncation notice:\n%s", d)
	}
}

func TestBuildDigest_CapsUnderThreshold(t *testing.T) {
	snap := sampleSnapshot()
	// Worst case: hundreds of elements with long names plus a long text tail.
	snap.Els = nil
	for i := 0; i < 500; i++ {
		snap.Els = append(snap.Els, elementInfo{
			Index: i, Tag: "a", Role: "link",
			Name: fmt.Sprintf("Some very long navigation label number %d with extra words", i),
			Href: fmt.Sprintf("/section/%d/deeply/nested/path/segment", i),
		})
	}
	snap.TotalEls = 500
	snap.Text = strings.Repeat("lorem ipsum dolor sit amet ", 1000)
	snap.TextLen = len(snap.Text)

	d := buildDigest(snap)
	if len(d) > maxDigestChars {
		t.Errorf("digest length %d exceeds cap %d", len(d), maxDigestChars)
	}
	// The auto-attach threshold in forge-core's loop is 8000; a digest must
	// never cross it or it would be attached as a file artifact.
	if len(d) > 8000 {
		t.Errorf("digest length %d exceeds forge-core largeToolOutputThreshold 8000", len(d))
	}
	if !strings.Contains(d, "of 500 elements") {
		t.Error("digest missing truncation notice under element pressure")
	}
}

func TestBuildDigest_TextTruncationHeader(t *testing.T) {
	snap := sampleSnapshot()
	snap.Text = strings.Repeat("x", 1200)
	snap.TextLen = 9400
	d := buildDigest(snap)
	if !strings.Contains(d, "of 9400 chars; browser_extract for more") {
		t.Errorf("digest missing text pagination hint:\n%s", d)
	}
}

func TestStaleRecovery_ContainsFreshDigest(t *testing.T) {
	msg := staleRecovery(sampleSnapshot())
	if !strings.HasPrefix(msg, "ERROR: stale element index") {
		t.Errorf("stale recovery missing error prefix: %q", msg[:60])
	}
	if !strings.Contains(msg, "Generation: 7") {
		t.Error("stale recovery missing fresh digest")
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"shot.png":           "shot.png",
		"../../etc/passwd":   "passwd",
		"a b/c:d.png":        "c_d.png",
		"..":                 "",
		"login page (1).png": "login_page__1_.png",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate_RuneSafe(t *testing.T) {
	s := "héllo wörld"
	got := truncate(s, 3)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate missing marker: %q", got)
	}
	// Must not split the é (2 bytes starting at index 1).
	if strings.ContainsRune(got, '�') {
		t.Errorf("truncate split a UTF-8 sequence: %q", got)
	}
	if truncate("short", 100) != "short" {
		t.Error("truncate modified a string under the cap")
	}
}
