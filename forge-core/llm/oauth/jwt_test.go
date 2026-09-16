package oauth

import (
	"encoding/base64"
	"testing"
	"time"
)

func makeJWT(t *testing.T, payload string) string {
	t.Helper()
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(payload) + "." + enc("sig")
}

func TestParseJWTExp(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()

	t.Run("valid exp", func(t *testing.T) {
		tok := makeJWT(t, `{"exp":`+itoa(future)+`,"sub":"x"}`)
		got, ok := ParseJWTExp(tok)
		if !ok {
			t.Fatal("expected ok=true for a JWT with exp")
		}
		if got.Unix() != future {
			t.Errorf("exp = %d, want %d", got.Unix(), future)
		}
	})

	t.Run("padded base64url tolerated", func(t *testing.T) {
		// StdEncoding for the payload emits '=' padding; ParseJWTExp must trim it.
		enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		payload := base64.URLEncoding.EncodeToString([]byte(`{"exp":` + itoa(future) + `}`)) // padded
		tok := enc(`{"alg":"none"}`) + "." + payload + "." + enc("sig")
		if _, ok := ParseJWTExp(tok); !ok {
			t.Error("expected padded payload to parse")
		}
	})

	t.Run("opaque non-JWT", func(t *testing.T) {
		if got, ok := ParseJWTExp("not-a-jwt"); ok || !got.IsZero() {
			t.Errorf("opaque token: got (%v, %v), want (zero, false)", got, ok)
		}
	})

	t.Run("no exp claim", func(t *testing.T) {
		tok := makeJWT(t, `{"sub":"x"}`)
		if _, ok := ParseJWTExp(tok); ok {
			t.Error("expected ok=false when exp is absent")
		}
	})

	t.Run("wrong segment count", func(t *testing.T) {
		if _, ok := ParseJWTExp("a.b"); ok {
			t.Error("expected ok=false for a 2-segment token")
		}
	})
}

// itoa avoids importing strconv just for the test literals.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
