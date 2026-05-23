package azure_ad

// ExtractTenantID returns the "tid" claim, or "" if it's missing/non-string.
// The empty-return form is intentional — callers want to distinguish
// "missing" from "wrong tenant," and a typed error here would be noise.
func ExtractTenantID(claims map[string]any) string {
	tid, _ := claims["tid"].(string)
	return tid
}
