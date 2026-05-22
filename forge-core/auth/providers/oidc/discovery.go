package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// providerMetadata is the subset of the OIDC discovery document we read.
// Per the OIDC spec, this document is stable for the lifetime of an issuer,
// so we cache it forever after the first successful load.
type providerMetadata struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// discovery lazily fetches and caches the OIDC discovery document.
//
// Concurrency model: a sync.Once gates the initial load so concurrent
// requests don't race. Subsequent calls return the cached result without
// further locking once `done` is true. If the first load fails, every
// caller sees the same error — but the next call re-attempts (we don't
// want a transient startup failure to permanently brick the provider).
type discovery struct {
	issuer string
	client *http.Client

	mu     sync.Mutex
	loaded bool
	meta   providerMetadata
}

func newDiscovery(issuer string, client *http.Client) *discovery {
	return &discovery{
		issuer: strings.TrimRight(issuer, "/"),
		client: client,
	}
}

// EnsureLoaded fetches the discovery doc on first call (or after a failed
// previous attempt). Safe for concurrent use; only one HTTP call is in
// flight per discovery instance at a time.
func (d *discovery) EnsureLoaded(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loaded {
		return nil
	}
	meta, err := d.fetch(ctx)
	if err != nil {
		return err
	}
	// Per OIDC spec, the discovery doc's `issuer` must match the issuer
	// the client used to fetch it. Catches misconfiguration where someone
	// points at a discovery doc for a different tenant.
	if meta.Issuer != d.issuer {
		return fmt.Errorf("oidc: discovery issuer %q does not match configured issuer %q", meta.Issuer, d.issuer)
	}
	if meta.JWKSURI == "" {
		return fmt.Errorf("oidc: discovery doc for %q has empty jwks_uri", d.issuer)
	}
	d.meta = meta
	d.loaded = true
	return nil
}

// JWKSURI returns the JWKS endpoint URL from the loaded discovery doc.
// Callers must call EnsureLoaded first.
func (d *discovery) JWKSURI() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.meta.JWKSURI
}

func (d *discovery) fetch(ctx context.Context) (providerMetadata, error) {
	url := d.issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return providerMetadata{}, fmt.Errorf("oidc: build discovery request: %w", err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return providerMetadata{}, fmt.Errorf("oidc: discovery fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return providerMetadata{}, fmt.Errorf("oidc: discovery returned status %d", resp.StatusCode)
	}
	var meta providerMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return providerMetadata{}, fmt.Errorf("oidc: discovery decode: %w", err)
	}
	return meta, nil
}
