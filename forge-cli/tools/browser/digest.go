package browser

import (
	"fmt"
	"strings"
)

// maxDigestChars keeps every digest under forge-core's largeToolOutputThreshold
// (8000): digests are intermediate observations for the LLM, never artifacts,
// and must also never hit the loop-level blunt truncation.
const maxDigestChars = 7500

// buildDigest renders a pageSnapshot as the compact indexed text the LLM
// navigates by. Format:
//
//	Page: Pricing — Vendor
//	URL: https://vendor.com/pricing
//	Generation: 7 (pass as "generation" to browser_click/browser_fill; indices reset when the page changes)
//
//	Interactive elements:
//	[0] link "Products" -> /products
//	[2] input(email) "Work email"
//	[3] input(password) "Password" ⚠ fill-protected
//	[14] select "Plan" = "Starter" [Starter, Pro, Enterprise]
//	(showing 100 of 143 elements — browser_state with a larger max_elements or scrolling to see more)
//
//	--- page text (first 1200 of 9400 chars; browser_extract for more) ---
//	...
func buildDigest(snap pageSnapshot) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Page: %s\n", strings.TrimSpace(snap.Title))
	fmt.Fprintf(&b, "URL: %s\n", snap.URL)
	fmt.Fprintf(&b, "Generation: %d (pass as \"generation\" to browser_click/browser_fill; indices reset when the page changes)\n\n", snap.Gen)

	if len(snap.Els) == 0 {
		b.WriteString("Interactive elements: none found\n")
	} else {
		b.WriteString("Interactive elements:\n")
		elBudget := maxDigestChars - b.Len() - 1600 // reserve room for the text section
		shown := 0
		for _, el := range snap.Els {
			line := formatElement(el)
			if elBudget-len(line)-1 < 0 {
				break
			}
			b.WriteString(line)
			b.WriteByte('\n')
			elBudget -= len(line) + 1
			shown++
		}
		if shown < snap.TotalEls {
			fmt.Fprintf(&b, "(showing %d of %d elements — browser_state with a larger max_elements or scrolling to see more)\n", shown, snap.TotalEls)
		}
	}

	text := strings.TrimSpace(snap.Text)
	if text != "" {
		remaining := maxDigestChars - b.Len() - 120 // header allowance
		if remaining > 200 {
			text = truncate(text, remaining)
			if snap.TextLen > len(text) {
				fmt.Fprintf(&b, "\n--- page text (first %d of %d chars; browser_extract for more) ---\n", len(text), snap.TextLen)
			} else {
				b.WriteString("\n--- page text ---\n")
			}
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}

	out := b.String()
	if len(out) > maxDigestChars {
		out = truncate(out, maxDigestChars)
	}
	return out
}

// formatElement renders one element line: `[i] role "name"` plus
// role-specific detail (href, input type, select options, protection mark).
func formatElement(el elementInfo) string {
	var b strings.Builder
	label := el.Role
	if el.Tag == "input" && el.InputType != "" && el.InputType != "text" {
		label = fmt.Sprintf("input(%s)", el.InputType)
	}
	fmt.Fprintf(&b, "[%d] %s %q", el.Index, label, el.Name)

	if el.Href != "" && el.Href != "#" {
		fmt.Fprintf(&b, " -> %s", truncate(el.Href, 80))
	}
	if el.Checked != nil {
		if *el.Checked {
			b.WriteString(" (checked)")
		} else {
			b.WriteString(" (unchecked)")
		}
	}
	if el.Tag == "select" {
		if el.Value != "" {
			fmt.Fprintf(&b, " = %q", el.Value)
		}
		if len(el.Options) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(el.Options, ", "))
		}
	}
	if el.Protected {
		b.WriteString(" ⚠ fill-protected")
	}
	return b.String()
}

// staleRecovery renders the standard stale-index error body: the error text
// plus a fresh digest so the LLM recovers in a single turn.
func staleRecovery(fresh pageSnapshot) string {
	return "ERROR: stale element index — the page changed since the last snapshot. Use the fresh state below (indices and generation have been reissued).\n\n" + buildDigest(fresh)
}
