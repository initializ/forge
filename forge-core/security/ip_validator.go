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
//
// Semantics:
//   - Always-blocked ranges (cloud metadata, loopback, "this" network) win
//     unconditionally — no allowlist punches a hole in them.
//   - If allowPrivate is true, RFC 1918 / link-local / CGNAT / IPv6 ULA are
//     all permitted (container/K8s posture).
//   - Otherwise, private ranges are blocked EXCEPT for IPs that fall inside
//     one of allowedPrivateCIDRs. That lets an operator open a narrow slice
//     of the private space (e.g. only 10.20.0.0/16) without opening RFC 1918
//     wholesale.
//
// Returns true (blocked) for nil IPs (fail closed).
func IsBlockedIP(ip net.IP, allowPrivate bool, allowedPrivateCIDRs []*net.IPNet) bool {
	if ip == nil {
		return true // fail closed
	}

	// Check IPv6 transition addresses that embed blocked IPv4
	if isBlockedIPv6Transition(ip, allowPrivate, allowedPrivateCIDRs) {
		return true
	}

	// Always-blocked wins: cloud metadata + loopback + "this" network are
	// never reachable, even if an operator adds them to allowedPrivateCIDRs.
	for _, n := range alwaysBlockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}

	if allowPrivate {
		return false
	}

	// Private-block path: honor the CIDR allowlist first, then fall through.
	for _, n := range allowedPrivateCIDRs {
		if n.Contains(ip) {
			return false
		}
	}

	for _, n := range privateBlockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}

	return false
}

// isBlockedIPv6Transition detects IPv6 transition addresses (NAT64, 6to4, Teredo)
// that embed blocked IPv4 addresses.
func isBlockedIPv6Transition(ip net.IP, allowPrivate bool, allowedPrivateCIDRs []*net.IPNet) bool {
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
		return IsBlockedIP(embedded.To4(), allowPrivate, allowedPrivateCIDRs)
	}

	// NAT64 extended: 64:ff9b:1::/48 — embedded IPv4 in last 4 bytes
	nat64ExtPrefix := []byte{0, 0x64, 0xff, 0x9b, 0, 0x01}
	if bytesEqual(ip16[:6], nat64ExtPrefix) {
		embedded := net.IP(ip16[12:16])
		return IsBlockedIP(embedded.To4(), allowPrivate, allowedPrivateCIDRs)
	}

	// 6to4: 2002::/16 — embedded IPv4 in bytes 2-5
	if ip16[0] == 0x20 && ip16[1] == 0x02 {
		embedded := net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5])
		return IsBlockedIP(embedded.To4(), allowPrivate, allowedPrivateCIDRs)
	}

	// Teredo: 2001:0000::/32 — XOR'd IPv4 in last 4 bytes
	if ip16[0] == 0x20 && ip16[1] == 0x01 && ip16[2] == 0x00 && ip16[3] == 0x00 {
		embedded := net.IPv4(ip16[12]^0xff, ip16[13]^0xff, ip16[14]^0xff, ip16[15]^0xff)
		return IsBlockedIP(embedded.To4(), allowPrivate, allowedPrivateCIDRs)
	}

	return false
}

// ParsePrivateCIDRs parses a list of CIDR strings into net.IPNet values.
// Returns an error naming the first invalid entry. Entries must be canonical
// CIDR notation (e.g. "10.0.0.0/8"); bare IPs are rejected — the intent is
// range-level exemption, not per-host holes.
//
// Non-canonical entries with host bits set (e.g. "10.20.0.5/16") are
// rejected too. `net.ParseCIDR` silently masks those to the network
// (10.20.0.0/16), which is the "wider than intended" direction — an operator
// who wrote "10.20.0.5/16" expecting a single host would instead get the
// whole /16 allowed. Failing loud here forces the operator to either write
// "10.20.0.5/32" (single host — explicit) or "10.20.0.0/16" (the range they
// really meant). #348 review nit 2.
func ParsePrivateCIDRs(cidrs []string) ([]*net.IPNet, error) {
	if len(cidrs) == 0 {
		return nil, nil
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		ip, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", s, err)
		}
		if !ip.Equal(n.IP) {
			return nil, fmt.Errorf("non-canonical CIDR %q: host bits are set — use %q for the range or %q for a single host",
				s, n.String(), ip.String()+singleHostMask(ip))
		}
		out = append(out, n)
	}
	return out, nil
}

// singleHostMask returns the /32 (IPv4) or /128 (IPv6) suffix that turns a
// bare IP into a single-host CIDR. Used only in error messages so the fix
// the operator needs is obvious in the log line.
func singleHostMask(ip net.IP) string {
	if ip.To4() != nil {
		return "/32"
	}
	return "/128"
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
