package providers

import (
	"net/http"

	"github.com/initializ/forge/forge-core/llm"
)

// setGatewayAPIKeyHeader implements the "apikey_header" outbound auth
// scheme (issue #302). When selected it sends the API key in a gateway
// header IN ADDITION TO the provider-native header, so an API gateway
// whose auth plugin reads a fixed header — e.g. Kong AI Gateway's
// key-auth, which reads `apikey` and ignores Authorization / x-api-key —
// authenticates the request while the upstream provider still receives
// (or has Kong inject) its native header.
//
// The header name defaults to llm.DefaultAPIKeyHeaderName ("apikey") and
// is overridable via ModelRef.auth_header_name for gateways with custom
// key_names. No-op for every other scheme, and when apiKey is empty.
func setGatewayAPIKeyHeader(req *http.Request, authScheme, headerName, apiKey string) {
	if authScheme != llm.AuthSchemeAPIKeyHeader || apiKey == "" {
		return
	}
	if headerName == "" {
		headerName = llm.DefaultAPIKeyHeaderName
	}
	req.Header.Set(headerName, apiKey)
}
