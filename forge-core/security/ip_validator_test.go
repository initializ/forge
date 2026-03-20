package security

import (
	"net"
	"testing"
)

func TestParseStrictIPv4(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"standard loopback", "127.0.0.1", true},
		{"standard private", "10.0.0.1", true},
		{"standard public", "8.8.8.8", true},
		{"zero", "0.0.0.0", true},
		{"max", "255.255.255.255", true},
		{"octal loopback", "0177.0.0.1", false},
		{"hex loopback", "0x7f.0.0.1", false},
		{"packed decimal", "2130706433", false},
		{"leading zero", "127.0.0.01", false},
		{"leading zero octet", "010.0.0.1", false},
		{"empty", "", false},
		{"hostname", "example.com", false},
		{"too many octets", "1.2.3.4.5", false},
		{"too few octets", "1.2.3", false},
		{"negative", "-1.0.0.1", false},
		{"overflow octet", "256.0.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStrictIPv4(tt.input)
			if tt.valid && got == nil {
				t.Errorf("ParseStrictIPv4(%q) = nil, want valid IP", tt.input)
			}
			if !tt.valid && got != nil {
				t.Errorf("ParseStrictIPv4(%q) = %v, want nil", tt.input, got)
			}
		})
	}
}

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		name         string
		ip           string
		allowPrivate bool
		blocked      bool
	}{
		// Always blocked
		{"nil IP", "", false, true},
		{"loopback", "127.0.0.1", false, true},
		{"loopback allowPrivate", "127.0.0.1", true, true},
		{"metadata", "169.254.169.254", false, true},
		{"metadata allowPrivate", "169.254.169.254", true, true},
		{"ipv6 loopback", "::1", false, true},
		{"ipv6 loopback allowPrivate", "::1", true, true},
		{"this network", "0.0.0.0", false, true},

		// Private ranges - blocked when allowPrivate=false
		{"rfc1918 10.x", "10.0.0.1", false, true},
		{"rfc1918 10.x allowPrivate", "10.0.0.1", true, false},
		{"rfc1918 172.16.x", "172.16.0.1", false, true},
		{"rfc1918 172.16.x allowPrivate", "172.16.0.1", true, false},
		{"rfc1918 192.168.x", "192.168.1.1", false, true},
		{"rfc1918 192.168.x allowPrivate", "192.168.1.1", true, false},
		{"link-local", "169.254.1.1", false, true},
		{"link-local allowPrivate", "169.254.1.1", true, false},
		{"cgnat", "100.64.0.1", false, true},
		{"cgnat allowPrivate", "100.64.0.1", true, false},
		{"ipv6 ula", "fd00::1", false, true},
		{"ipv6 ula allowPrivate", "fd00::1", true, false},
		{"ipv6 link-local", "fe80::1", false, true},
		{"ipv6 link-local allowPrivate", "fe80::1", true, false},

		// Public IPs - never blocked
		{"public ipv4", "8.8.8.8", false, false},
		{"public ipv4 allowPrivate", "8.8.8.8", true, false},
		{"public ipv6", "2607:f8b0:4004:800::200e", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ip net.IP
			if tt.ip != "" {
				ip = net.ParseIP(tt.ip)
			}
			got := IsBlockedIP(ip, tt.allowPrivate)
			if got != tt.blocked {
				t.Errorf("IsBlockedIP(%v, %v) = %v, want %v", ip, tt.allowPrivate, got, tt.blocked)
			}
		})
	}
}

func TestIsBlockedIPv6Transition(t *testing.T) {
	tests := []struct {
		name         string
		ip           string
		allowPrivate bool
		blocked      bool
	}{
		// NAT64: 64:ff9b::<ipv4> — loopback embedded
		{"nat64 loopback", "64:ff9b::127.0.0.1", false, true},
		{"nat64 loopback allowPrivate", "64:ff9b::127.0.0.1", true, true},
		// NAT64 with private
		{"nat64 private", "64:ff9b::10.0.0.1", false, true},
		{"nat64 private allowPrivate", "64:ff9b::10.0.0.1", true, false},
		// NAT64 with public
		{"nat64 public", "64:ff9b::8.8.8.8", false, false},
		// NAT64 with metadata
		{"nat64 metadata", "64:ff9b::169.254.169.254", false, true},
		{"nat64 metadata allowPrivate", "64:ff9b::169.254.169.254", true, true},

		// 6to4: 2002:<ipv4>::
		{"6to4 loopback", "2002:7f00:0001::", false, true},
		{"6to4 private", "2002:0a00:0001::", false, true},
		{"6to4 private allowPrivate", "2002:0a00:0001::", true, false},
		{"6to4 public", "2002:0808:0808::", false, false},
		{"6to4 metadata", "2002:a9fe:a9fe::", false, true},

		// Teredo: 2001:0000::<server>:<flags>:<xor'd client>
		// Teredo XORs client IPv4 with 0xFFFFFFFF
		// 127.0.0.1 XOR'd = 0x80ffff fe = (128.255.255.254)
		{"teredo loopback", "2001:0000:4136:e378:8000:63bf:80ff:fffe", false, true},
		// 10.0.0.1 XOR'd = 0xf5fffffe = (245.255.255.254)
		{"teredo private", "2001:0000:4136:e378:8000:63bf:f5ff:fffe", false, true},
		{"teredo private allowPrivate", "2001:0000:4136:e378:8000:63bf:f5ff:fffe", true, false},

		// Regular IPv6 — not transition
		{"regular ipv6", "2607:f8b0:4004:800::200e", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			got := IsBlockedIP(ip, tt.allowPrivate)
			if got != tt.blocked {
				t.Errorf("IsBlockedIP(%v, %v) = %v, want %v", ip, tt.allowPrivate, got, tt.blocked)
			}
		})
	}
}

func TestValidateHostIP(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"standard ipv4", "127.0.0.1", false},
		{"public ipv4", "8.8.8.8", false},
		{"hostname", "example.com", false},
		{"ipv6", "::1", false},
		{"octal", "0177.0.0.1", true},
		{"hex", "0x7f000001", true},
		{"packed decimal", "2130706433", true},
		{"leading zero", "127.0.0.01", true},
		{"leading zero octet", "010.0.0.1", true},
		{"empty", "", false},
		{"subdomain", "api.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHostIP(tt.host)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateHostIP(%q) = nil, want error", tt.host)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateHostIP(%q) = %v, want nil", tt.host, err)
			}
		})
	}
}

func TestLooksLikeIP(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"2130706433", true},       // packed decimal
		{"0x7f000001", true},       // hex
		{"0X7F000001", true},       // hex uppercase
		{"0177.0.0.1", true},       // octal-looking
		{"127.0.0.01", true},       // leading zero
		{"127.0.0.1", false},       // valid strict IPv4 — not "suspicious"
		{"example.com", false},     // hostname
		{"", false},                // empty
		{"10.0.0.1", false},        // valid strict IPv4
		{"api.example.com", false}, // hostname with dots
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := looksLikeIP(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeIP(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
