package oauth

import (
	"testing"
	"time"
)

func TestToken_IsExpired(t *testing.T) {
	tests := []struct {
		name    string
		token   Token
		expired bool
	}{
		{
			name:    "zero expiry is expired",
			token:   Token{},
			expired: true,
		},
		{
			name:    "future expiry is not expired",
			token:   Token{ExpiresAt: time.Now().Add(10 * time.Minute)},
			expired: false,
		},
		{
			name:    "past expiry is expired",
			token:   Token{ExpiresAt: time.Now().Add(-1 * time.Minute)},
			expired: true,
		},
		{
			name:    "within 5min buffer is expired",
			token:   Token{ExpiresAt: time.Now().Add(3 * time.Minute)},
			expired: true,
		},
		{
			name:    "just outside 5min buffer is not expired",
			token:   Token{ExpiresAt: time.Now().Add(6 * time.Minute)},
			expired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.token.IsExpired()
			if got != tt.expired {
				t.Errorf("IsExpired() = %v, want %v", got, tt.expired)
			}
		})
	}
}

func TestToken_IsExpiredWithBuffer(t *testing.T) {
	token := Token{ExpiresAt: time.Now().Add(30 * time.Second)}

	if !token.IsExpiredWithBuffer(1 * time.Minute) {
		t.Error("should be expired with 1 minute buffer")
	}
	if token.IsExpiredWithBuffer(10 * time.Second) {
		t.Error("should not be expired with 10 second buffer")
	}
}
