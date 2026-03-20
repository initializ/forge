package security

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// strictIPv4Re matches exactly four dotted-decimal octets (no leading zeros, 0-255).
var strictIPv4Re = regexp.MustCompile(
	`^(25[0-5]|2[0-4]\d|1\d\d|[1-9]\d|\d)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]\d|\d)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]\d|\d)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]\d|\d)$`,
)

// alwaysBlockedCIDRs are blocked regardless of allowPrivate setting.
// Cloud metadata and loopback must never be reachable.
var alwaysBlockedCIDRs []*net.IPNet

// privateBlockedCIDRs are blocked only when allowPrivate is false.
var privateBlockedCIDRs []*net.IPNet

func init() {
	for _, cidr := range []string{
		"169.254.169.254/32", // cloud metadata endpoint
		"127.0.0.0/8",        // IPv4 loopback
		"::1/128",            // IPv6 loopback
		"0.0.0.0/8",          // "this" network
	} {
		_, n, _ := net.ParseCIDR(cidr)
		alwaysBlockedCIDRs = append(alwaysBlockedCIDRs, n)
	}
	for _, cidr := range []string{
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"169.254.0.0/16", // link-local
		"100.64.0.0/10",  // CGNAT
		"fc00::/7",       // IPv6 ULA
		"fe80::/10",      // IPv6 link-local
	} {
		_, n, _ := net.ParseCIDR(cidr)
		privateBlockedCIDRs = append(privateBlockedCIDRs, n)
	}
}

// ParseStrictIPv4 parses an IPv4 address in strict dotted-decimal notation.
// It rejects octal (0177.0.0.1), hex (0x7f.0.0.1), packed decimal (2130706433),
// and leading-zero forms (127.0.0.01). Returns nil if the input is not a valid
// strict IPv4 address.
func ParseStrictIPv4(s string) net.IP {
	if !strictIPv4Re.MatchString(s) {
		return nil
	}
	return net.ParseIP(s).To4()
}

// IsBlockedIP checks whether an IP is in a blocked CIDR range.
// When allowPrivate is true, RFC 1918 and link-local ranges are permitted
// (for container/K8s environments), but cloud metadata and loopback are
// always blocked. Returns true (blocked) for nil IPs (fail closed).
func IsBlockedIP(ip net.IP, allowPrivate bool) bool {
	if ip == nil {
		return true // fail closed
	}

	// Check IPv6 transition addresses that embed blocked IPv4
	if isBlockedIPv6Transition(ip, allowPrivate) {
		return true
	}

	for _, n := range alwaysBlockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}

	if !allowPrivate {
		for _, n := range privateBlockedCIDRs {
			if n.Contains(ip) {
				return true
			}
		}
	}

	return false
}

// isBlockedIPv6Transition detects IPv6 transition addresses (NAT64, 6to4, Teredo)
// that embed blocked IPv4 addresses.
func isBlockedIPv6Transition(ip net.IP, allowPrivate bool) bool {
	// Ensure we're working with a 16-byte representation
	ip16 := ip.To16()
	if ip16 == nil {
		return false
	}
	// Skip if this is actually an IPv4 address (mapped or native)
	if ip.To4() != nil {
		return false
	}

	// NAT64: 64:ff9b::/96 — embedded IPv4 in last 4 bytes
	nat64Prefix := []byte{0, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0}
	if bytesEqual(ip16[:12], nat64Prefix) {
		embedded := net.IP(ip16[12:16])
		return IsBlockedIP(embedded.To4(), allowPrivate)
	}

	// NAT64 extended: 64:ff9b:1::/48 — embedded IPv4 in last 4 bytes
	nat64ExtPrefix := []byte{0, 0x64, 0xff, 0x9b, 0, 0x01}
	if bytesEqual(ip16[:6], nat64ExtPrefix) {
		embedded := net.IP(ip16[12:16])
		return IsBlockedIP(embedded.To4(), allowPrivate)
	}

	// 6to4: 2002::/16 — embedded IPv4 in bytes 2-5
	if ip16[0] == 0x20 && ip16[1] == 0x02 {
		embedded := net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5])
		return IsBlockedIP(embedded.To4(), allowPrivate)
	}

	// Teredo: 2001:0000::/32 — XOR'd IPv4 in last 4 bytes
	if ip16[0] == 0x20 && ip16[1] == 0x01 && ip16[2] == 0x00 && ip16[3] == 0x00 {
		embedded := net.IPv4(ip16[12]^0xff, ip16[13]^0xff, ip16[14]^0xff, ip16[15]^0xff)
		return IsBlockedIP(embedded.To4(), allowPrivate)
	}

	return false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// looksLikeIP returns true if the string looks like it might be a non-standard
// IP representation: digit-only strings (packed decimal), 0x prefix (hex),
// or digit+dot strings that fail strict IPv4 parsing.
func looksLikeIP(s string) bool {
	if s == "" {
		return false
	}
	// Hex prefix
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return true
	}

	hasLetter := false
	hasDot := false
	allDigitsOrDots := true
	for _, c := range s {
		if c == '.' {
			hasDot = true
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		allDigitsOrDots = false
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' {
			hasLetter = true
		}
	}

	// Pure digit string like "2130706433" (packed decimal)
	if allDigitsOrDots && !hasDot {
		return true
	}

	// Digit+dot string that failed strict parse means octal or leading-zero
	if allDigitsOrDots && hasDot && !hasLetter {
		return ParseStrictIPv4(s) == nil
	}

	return false
}

// ValidateHostIP validates that a hostname is not using a non-standard IP format
// that could bypass security checks. It rejects octal, hex, packed decimal, and
// leading-zero IP representations.
func ValidateHostIP(host string) error {
	// If it's a valid strict IPv4, it's fine (checked elsewhere for blocked ranges)
	if ParseStrictIPv4(host) != nil {
		return nil
	}

	// If it's a standard IPv6, it's fine
	if net.ParseIP(host) != nil {
		return nil
	}

	// Check if it looks like a non-standard IP format
	if looksLikeIP(host) {
		return fmt.Errorf("rejected non-standard IP format: %q", host)
	}

	return nil
}
