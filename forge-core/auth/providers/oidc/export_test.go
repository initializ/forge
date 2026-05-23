package oidc

import "time"

// SetCacheClockForTest replaces the JWKS cache's time source. Exposed only
// in _test builds so tests can advance virtual time past TTL boundaries
// without sleeping.
//
// Not part of the public API. Test code in this package's external test
// file (oidc_test) uses this via the package-import boundary.
func SetCacheClockForTest(p *Provider, now func() time.Time) {
	p.jwks.now = now
}
