package security

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// TCPMatcher enforces port-aware allowlist entries for raw-TCP egress.
//
// Entries have the shape `host:port` (or `host:*` for any-port on that host).
// Host portion supports the same exact + wildcard-suffix rules as
// DomainMatcher, so `*.brokers.internal:9092` matches
// `broker1.brokers.internal:9092` on the exact declared port.
//
// The matcher is the port-carrying peer of DomainMatcher: DomainMatcher covers
// HTTP(S) traffic (where the transport carries no port granularity), and
// TCPMatcher covers raw-TCP flows through the SOCKS5 gate where the client
// negotiates a specific host:port pair. Both matchers are consulted at the
// dial gate — a target passes if either matcher allows it.
type TCPMatcher struct {
	// exact holds `host:port` entries with a concrete host and port.
	exact map[string]struct{}
	// exactAnyPort holds host entries with wildcard port (`host:*`).
	exactAnyPort map[string]struct{}
	// wildcardHosts holds `.suffix:port` pairs (leading `*.` stripped).
	// A hostname matching the suffix on the exact port is allowed.
	wildcardHosts []tcpWildcard
	// wildcardAnyPort holds `.suffix` entries with wildcard port (`*.suffix:*`).
	wildcardAnyPort []string
}

type tcpWildcard struct {
	suffix string // e.g. ".brokers.internal"
	port   string // e.g. "9092"
}

// NewTCPMatcher parses the `allowed_tcp` config entries into a matcher.
// Entries must be `host:port` or `host:*` (bare host without port is rejected).
// Port `0` and ports outside 1–65535 are rejected. Returns an error naming
// the first invalid entry so bad config trips at load, not at first dial.
func NewTCPMatcher(entries []string) (*TCPMatcher, error) {
	m := &TCPMatcher{
		exact:        make(map[string]struct{}),
		exactAnyPort: make(map[string]struct{}),
	}
	for _, raw := range entries {
		host, port, err := parseTCPEntry(raw)
		if err != nil {
			return nil, err
		}
		host = strings.ToLower(host)

		isWildHost := strings.HasPrefix(host, "*.")
		if isWildHost {
			suffix := host[1:] // ".brokers.internal"
			if port == "*" {
				m.wildcardAnyPort = append(m.wildcardAnyPort, suffix)
			} else {
				m.wildcardHosts = append(m.wildcardHosts, tcpWildcard{suffix: suffix, port: port})
			}
			continue
		}
		if port == "*" {
			m.exactAnyPort[host] = struct{}{}
		} else {
			m.exact[host+":"+port] = struct{}{}
		}
	}
	return m, nil
}

// IsAllowed returns true if the (host, port) pair matches any configured
// entry. Host is compared case-insensitively.
func (m *TCPMatcher) IsAllowed(host, port string) bool {
	if m == nil {
		return false
	}
	host = strings.ToLower(host)
	if _, ok := m.exact[host+":"+port]; ok {
		return true
	}
	if _, ok := m.exactAnyPort[host]; ok {
		return true
	}
	for _, w := range m.wildcardHosts {
		if w.port == port && strings.HasSuffix(host, w.suffix) {
			return true
		}
	}
	for _, suffix := range m.wildcardAnyPort {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// Empty reports whether the matcher has zero configured entries. Callers use
// this to skip starting a SOCKS5 listener when raw-TCP egress isn't configured
// — the listener is unnecessary and its port is one more thing to reason
// about at deploy time.
func (m *TCPMatcher) Empty() bool {
	if m == nil {
		return true
	}
	return len(m.exact) == 0 &&
		len(m.exactAnyPort) == 0 &&
		len(m.wildcardHosts) == 0 &&
		len(m.wildcardAnyPort) == 0
}

// parseTCPEntry splits `host:port` and validates both sides. Port `*` is
// permitted (any-port on that host).
//
// IPv6 literals must be bracketed (`[::1]:5432`) — brackets are stripped
// during parse so the stored host matches what the SOCKS5 IPv6 ATYP path
// produces (`net.IP(buf).String()` yields `::1` without brackets). This
// closes the doc/functionality gap flagged in the #355 review: pre-fix,
// `[::1]:5432` was stored with brackets and never matched at runtime.
func parseTCPEntry(raw string) (host, port string, err error) {
	if raw == "" {
		return "", "", fmt.Errorf("tcp allowlist: empty entry")
	}
	// Special-case wildcard port: `net.SplitHostPort` treats `*` as a valid
	// port string (it doesn't validate numeric), so we can lean on it for
	// bracket-handling on the host portion. `[::1]:*` → ("::1", "*").
	// `db.internal:*` → ("db.internal", "*"). Wildcard host with wildcard
	// port `*.brokers.internal:*` also splits correctly — the `*.` prefix
	// is opaque to SplitHostPort.
	host, port, err = net.SplitHostPort(raw)
	if err != nil {
		// Reword to name the offending entry so the operator sees which
		// config line failed. SplitHostPort's default error strings don't
		// include the input.
		return "", "", fmt.Errorf("tcp allowlist: entry %q: %w (use host:port or host:*, bracket IPv6 like [::1]:5432)", raw, err)
	}
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		return "", "", fmt.Errorf("tcp allowlist: entry %q has empty host", raw)
	}
	if port == "" {
		return "", "", fmt.Errorf("tcp allowlist: entry %q has empty port", raw)
	}
	if port != "*" {
		n, convErr := strconv.Atoi(port)
		if convErr != nil {
			return "", "", fmt.Errorf("tcp allowlist: entry %q has non-numeric port %q", raw, port)
		}
		if n < 1 || n > 65535 {
			return "", "", fmt.Errorf("tcp allowlist: entry %q port %d out of range (1-65535)", raw, n)
		}
	}
	return host, port, nil
}
