package providers

import "net/url"

// sanitizeEndpoint strips any userinfo (user:password@) from a URL before it is
// recorded on an audit event (llm.ChatResponse.Endpoint) or named in an error
// string. A gateway base URL configured with embedded credentials
// (https://user:pass@gateway) is a legitimate setup — Go's http client extracts
// the userinfo into a Basic auth header — but the raw URL with the password
// must never reach the audit stream or the logs. Returns the input unchanged if
// it can't be parsed.
func sanitizeEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}
