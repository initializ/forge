package security

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Minimal SOCKS5v5 server (RFC 1928) for validated raw-TCP egress.
//
// Supported: CONNECT command; DOMAIN / IPv4 / IPv6 ATYP; single "no auth"
// method.
//
// Rejected: BIND, UDP ASSOCIATE, any auth method other than 0x00. We deny
// UDP because the current dial-gate primitive only covers TCP and we want a
// small explicit surface — expanding to UDP ASSOCIATE is a follow-up when a
// concrete use case (e.g. QUIC broker) appears.
//
// The server reads the SOCKS5 destination address from the client, hands it
// to `ValidateAndDial` (the same primitive `handleConnect` uses), then blind-
// relays bytes both ways until either side closes. Audit is emitted by
// `ValidateAndDial` — the SOCKS handler itself never fires the hook.

const (
	socks5Ver byte = 0x05

	// Methods
	methodNoAuth       byte = 0x00
	methodNoAcceptable byte = 0xff

	// Commands
	cmdConnect      byte = 0x01
	cmdBind         byte = 0x02
	cmdUDPAssociate byte = 0x03

	// Address types
	atypIPv4   byte = 0x01
	atypDomain byte = 0x03
	atypIPv6   byte = 0x04

	// Reply codes
	repSuccess           byte = 0x00
	repGeneralFailure    byte = 0x01
	repConnectionDenied  byte = 0x02 // "connection not allowed by ruleset"
	repHostUnreachable   byte = 0x04
	repCommandNotSupport byte = 0x07
	repATYPNotSupport    byte = 0x08
)

// handleSOCKS5 runs one client connection through the SOCKS5 handshake and,
// on success, blind-relays bytes to the upstream. It never leaks the upstream
// conn or the client conn — both are closed on every exit path.
func (p *EgressProxy) handleSOCKS5(client net.Conn) {
	defer client.Close() //nolint:errcheck

	// Bound the handshake so a slow / malicious client can't tie up a socket
	// forever. The relay itself uses no deadline — long-lived TCP flows
	// (Kafka, Postgres pooler) are the whole point.
	if err := client.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return
	}

	if err := socks5Greeting(client); err != nil {
		return
	}

	host, port, err := socks5ReadRequest(client)
	if err != nil {
		// If the client sent a well-formed request with an unsupported ATYP or
		// CMD, socks5ReadRequest already wrote the reply. Any other read/parse
		// error means the connection is toast — no reply.
		return
	}

	// Clear the handshake deadline before the relay so long-lived flows aren't
	// cut off. Bytes.Copy on both directions will exit when either side closes.
	_ = client.SetDeadline(time.Time{})

	// SOCKS5 has no per-request context — the handshake owns the conn.
	// Use context.Background so cancellation from server shutdown flows
	// through via listener close (relayPair exits when either side closes).
	upstream, dialErr := p.ValidateAndDial(context.Background(), host, port)
	if dialErr != nil {
		rep := repConnectionDenied
		if errors.Is(dialErr, errHostUnreachable) {
			rep = repHostUnreachable
		}
		_ = socks5WriteReply(client, rep, net.IPv4zero, 0)
		return
	}
	defer upstream.Close() //nolint:errcheck

	// Reply with BND.ADDR = local side of the upstream socket. Clients
	// generally ignore this but the protocol requires it.
	bndIP, bndPort := extractBoundAddr(upstream.LocalAddr())
	if err := socks5WriteReply(client, repSuccess, bndIP, bndPort); err != nil {
		return
	}

	relayPair(client, upstream)
}

// socks5Greeting completes the method negotiation: read [ver, nmethods,
// methods...], reply with [ver, methodNoAuth] if the client offered no-auth,
// otherwise [ver, methodNoAcceptable] and error out.
func socks5Greeting(client net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil {
		return fmt.Errorf("socks5 greeting: %w", err)
	}
	if header[0] != socks5Ver {
		return fmt.Errorf("socks5 greeting: unsupported version 0x%02x", header[0])
	}
	nmethods := int(header[1])
	if nmethods == 0 {
		_, _ = client.Write([]byte{socks5Ver, methodNoAcceptable})
		return fmt.Errorf("socks5 greeting: client offered zero methods")
	}
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(client, methods); err != nil {
		return fmt.Errorf("socks5 greeting: %w", err)
	}
	for _, m := range methods {
		if m == methodNoAuth {
			_, _ = client.Write([]byte{socks5Ver, methodNoAuth})
			return nil
		}
	}
	_, _ = client.Write([]byte{socks5Ver, methodNoAcceptable})
	return fmt.Errorf("socks5 greeting: client didn't offer no-auth method")
}

// socks5ReadRequest reads the request PDU and returns the destination
// host:port. On unsupported command / address type, it writes the appropriate
// reply and returns an error.
func socks5ReadRequest(client net.Conn) (host, port string, err error) {
	// Fixed header: [ver, cmd, rsv, atyp]
	fixed := make([]byte, 4)
	if _, err := io.ReadFull(client, fixed); err != nil {
		return "", "", fmt.Errorf("socks5 request header: %w", err)
	}
	if fixed[0] != socks5Ver {
		return "", "", fmt.Errorf("socks5 request: unsupported version 0x%02x", fixed[0])
	}
	if fixed[1] != cmdConnect {
		// BIND / UDP ASSOCIATE are explicitly out of scope for this handler.
		_ = socks5WriteReply(client, repCommandNotSupport, net.IPv4zero, 0)
		return "", "", fmt.Errorf("socks5 request: command 0x%02x not supported", fixed[1])
	}
	// fixed[2] is RSV — ignored.

	switch fixed[3] {
	case atypIPv4:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(client, buf); err != nil {
			return "", "", fmt.Errorf("socks5 request ipv4: %w", err)
		}
		host = net.IP(buf).String()
	case atypIPv6:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(client, buf); err != nil {
			return "", "", fmt.Errorf("socks5 request ipv6: %w", err)
		}
		host = net.IP(buf).String()
	case atypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(client, lenBuf); err != nil {
			return "", "", fmt.Errorf("socks5 request domain length: %w", err)
		}
		dlen := int(lenBuf[0])
		if dlen == 0 {
			return "", "", fmt.Errorf("socks5 request: zero-length domain")
		}
		buf := make([]byte, dlen)
		if _, err := io.ReadFull(client, buf); err != nil {
			return "", "", fmt.Errorf("socks5 request domain: %w", err)
		}
		host = string(buf)
	default:
		_ = socks5WriteReply(client, repATYPNotSupport, net.IPv4zero, 0)
		return "", "", fmt.Errorf("socks5 request: address type 0x%02x not supported", fixed[3])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(client, portBuf); err != nil {
		return "", "", fmt.Errorf("socks5 request port: %w", err)
	}
	port = strconv.Itoa(int(binary.BigEndian.Uint16(portBuf)))
	return host, port, nil
}

// socks5WriteReply emits the fixed 10-byte IPv4 reply (or 22-byte IPv6). We
// always encode BND.ADDR as IPv4 when the bound IP is IPv4 (or zero); this
// matches what most clients expect and keeps the wire simple.
func socks5WriteReply(client net.Conn, rep byte, bndIP net.IP, bndPort uint16) error {
	var buf []byte
	if v4 := bndIP.To4(); v4 != nil {
		buf = make([]byte, 10)
		buf[3] = atypIPv4
		copy(buf[4:8], v4)
		binary.BigEndian.PutUint16(buf[8:10], bndPort)
	} else {
		buf = make([]byte, 22)
		buf[3] = atypIPv6
		copy(buf[4:20], bndIP.To16())
		binary.BigEndian.PutUint16(buf[20:22], bndPort)
	}
	buf[0] = socks5Ver
	buf[1] = rep
	buf[2] = 0x00 // RSV
	_, err := client.Write(buf)
	return err
}

// extractBoundAddr returns the IP+port from a net.Addr for BND.ADDR. Zero
// values are fine when the local socket has none (e.g. Unix sockets in tests).
func extractBoundAddr(addr net.Addr) (net.IP, uint16) {
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp != nil {
		if tcp.IP != nil {
			return tcp.IP, uint16(tcp.Port)
		}
	}
	return net.IPv4zero, 0
}
