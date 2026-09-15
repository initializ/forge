package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeJWTWithTyp builds a compact-JWS-shaped token (header.payload.sig) whose
// header carries the given `typ` (omitted when ""). The signature segment is a
// placeholder — the typ check reads only the unverified header.
func makeJWTWithTyp(t *testing.T, typ string) string {
	t.Helper()
	hdr := map[string]any{"alg": "none"}
	if typ != "" {
		hdr["typ"] = typ
	}
	h, _ := json.Marshal(hdr)
	p, _ := json.Marshal(map[string]any{"sub": "x"})
	enc := base64.RawURLEncoding.EncodeToString
	return enc(h) + "." + enc(p) + ".sig"
}

func TestIsRejectedInboundTokenType(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"chain token rejected", makeJWTWithTyp(t, MediaTypeChainToken), true},
		{"workload credential rejected", makeJWTWithTyp(t, MediaTypeWorkloadCredential), true},
		{"mandate rejected", makeJWTWithTyp(t, MediaTypeMandate), true},
		{"platform bearer accepted", makeJWTWithTyp(t, MediaTypePlatformBearer), false},
		{"no typ header accepted", makeJWTWithTyp(t, ""), false},
		{"unknown typ accepted", makeJWTWithTyp(t, "application/at+jwt"), false},
		{"opaque non-jwt accepted", "not-a-jwt-opaque-secret", false},
		{"two-segment non-jwt accepted", "aaa.bbb", false},
		{"garbage header segment accepted", "!!!.bbb.ccc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRejectedInboundTokenType(tc.token); got != tc.want {
				t.Errorf("IsRejectedInboundTokenType(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

// recordingProvider accepts ANY token and records whether Verify ran — so a
// test can prove the typ gate rejects BEFORE the provider chain is consulted.
type recordingProvider struct {
	called bool
}

func (p *recordingProvider) Name() string { return "recording" }
func (p *recordingProvider) Verify(_ context.Context, _ string, _ Headers) (*Identity, error) {
	p.called = true
	id := Identity{UserID: "u1", Source: "recording"}
	return &id, nil
}

func TestMiddleware_RejectsWrongTokenType_BeforeChain(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("chain token is 401 and never reaches the provider", func(t *testing.T) {
		prov := &recordingProvider{}
		mw := Middleware(MiddlewareOptions{Chain: NewChainProvider(prov), SkipPaths: DefaultSkipPaths()})
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("Authorization", "Bearer "+makeJWTWithTyp(t, MediaTypeChainToken))
		rec := httptest.NewRecorder()
		mw(okHandler).ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
		if prov.called {
			t.Error("provider chain must NOT be consulted for a cross-use token — typ reject happens first")
		}
	})

	t.Run("platform-bearer passes through to the chain", func(t *testing.T) {
		prov := &recordingProvider{}
		mw := Middleware(MiddlewareOptions{Chain: NewChainProvider(prov), SkipPaths: DefaultSkipPaths()})
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("Authorization", "Bearer "+makeJWTWithTyp(t, MediaTypePlatformBearer))
		rec := httptest.NewRecorder()
		mw(okHandler).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if !prov.called {
			t.Error("a platform-bearer token must reach the provider chain")
		}
	})

	t.Run("opaque (non-JWT) token skips the typ gate and reaches the chain", func(t *testing.T) {
		prov := &recordingProvider{}
		mw := Middleware(MiddlewareOptions{Chain: NewChainProvider(prov), SkipPaths: DefaultSkipPaths()})
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("Authorization", "Bearer opaque-loopback-secret")
		rec := httptest.NewRecorder()
		mw(okHandler).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (opaque tokens must not be typ-gated)", rec.Code)
		}
		if !prov.called {
			t.Error("opaque token must reach the provider chain")
		}
	})
}

func TestMiddleware_WrongTokenType_AuditReason(t *testing.T) {
	var gotErr error
	var gotKind string
	mw := Middleware(MiddlewareOptions{
		Chain:     NewChainProvider(&recordingProvider{}),
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(_ *http.Request, _ *Identity, err error, kind string) {
			gotErr = err
			gotKind = kind
		},
	})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWTWithTyp(t, MediaTypeMandate))
	mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)

	if FailReason(gotErr) != "wrong_token_type" {
		t.Errorf("audit fail reason = %q, want wrong_token_type", FailReason(gotErr))
	}
	if gotKind != "jwt" {
		t.Errorf("token_kind = %q, want jwt", gotKind)
	}
}
