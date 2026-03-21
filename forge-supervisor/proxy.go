package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/initializ/forge/forge-core/security"
)

// TransparentProxy intercepts redirected TCP traffic, extracts the target hostname
// via SNI or HTTP Host header, checks against the domain matcher, and either
// forwards or denies the connection.
type TransparentProxy struct {
	listener      net.Listener
	matcher      *security.DomainMatcher
	denialTracker *DenialTracker
	audit         *AuditLogger
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

// NewTransparentProxy creates a new transparent TCP proxy.
func NewTransparentProxy(matcher *security.DomainMatcher, denialTracker *DenialTracker, audit *AuditLogger) *TransparentProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &TransparentProxy{
		matcher:      matcher,
		denialTracker: denialTracker,
		audit:         audit,
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Start begins listening and accepting connections.
func (p *TransparentProxy) Start(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy listen: %w", err)
	}
	p.listener = ln

	log.Printf("INFO: transparent proxy listening on %s", ln.Addr().String())

	p.wg.Add(1)
	go p.acceptLoop()

	return nil
}

// Stop gracefully shuts down the proxy.
func (p *TransparentProxy) Stop() error {
	p.cancel()
	if p.listener != nil {
		p.listener.Close()
	}
	p.wg.Wait()
	return nil
}

func (p *TransparentProxy) acceptLoop() {
	defer p.wg.Done()

	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.ctx.Done():
				return
			default:
				log.Printf("ERROR: proxy accept: %v", err)
				continue
			}
		}

		p.wg.Add(1)
		go p.handleConnection(conn)
	}
}

func (p *TransparentProxy) handleConnection(client net.Conn) {
	defer p.wg.Done()
	defer client.Close()

	// Get original destination from redirected connection
	origAddr, err := getOriginalDst(client)
	if err != nil {
		log.Printf("ERROR: get original dst: %v", err)
		return
	}

	origTCPAddr, ok := origAddr.(*net.TCPAddr)
	if !ok {
		log.Printf("ERROR: unexpected addr type: %T", origAddr)
		return
	}

	// Extract host and any consumed bytes (for replay)
	host, consumed, allowed := p.extractAndCheck(client, origTCPAddr)

	if !allowed {
		p.denialTracker.Add(DenialEvent{
			Timestamp: time.Now().UTC(),
			Host:      host,
			Port:      origTCPAddr.Port,
		})

		p.audit.Log(&AuditEvent{
			Timestamp: time.Now().UTC(),
			Action:    "denied",
			Host:      host,
			Port:      origTCPAddr.Port,
		})

		fmt.Fprintf(client, "HTTP/1.1 403 Forbidden\r\n\r\n")
		return
	}

	// Log allowed connection
	p.audit.Log(&AuditEvent{
		Timestamp: time.Now().UTC(),
		Action:    "allowed",
		Host:      host,
		Port:      origTCPAddr.Port,
	})

	// Dial original destination
	upstream, err := net.DialTimeout("tcp", origTCPAddr.String(), 10*time.Second)
	if err != nil {
		log.Printf("ERROR: dial upstream %s: %v", origTCPAddr, err)
		return
	}
	defer upstream.Close()

	// Replay consumed bytes to upstream, then relay the rest
	p.relay(client, upstream, consumed)
}

// extractAndCheck peeks at the first bytes to determine TLS vs HTTP,
// extracts the hostname, and checks against the allowlist.
// Returns host, consumed bytes (for replay), and whether it's allowed.
func (p *TransparentProxy) extractAndCheck(conn net.Conn, addr *net.TCPAddr) (string, []byte, bool) {
	// Peek at first 5 bytes (TLS record header max)
	firstBytes := make([]byte, 5)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	n, err := conn.Read(firstBytes)
	if err != nil && err != io.EOF {
		log.Printf("ERROR: peek bytes: %v", err)
		return fmt.Sprintf("%s:%d", addr.IP, addr.Port), nil, false
	}

	var host string
	var consumed []byte

	if n >= 3 && firstBytes[0] == 0x16 && firstBytes[1] == 0x03 {
		// TLS ClientHello
		sniBytes, sniHost := ExtractSNIFromClientHello(firstBytes[:n], conn)
		if sniHost != "" {
			host = sniHost
			consumed = append(consumed, sniBytes...)
		} else {
			host = fmt.Sprintf("%s:%d", addr.IP, addr.Port)
		}
	} else {
		// HTTP — read Host header, replaying consumed bytes first
		httpBytes, httpHost := ExtractHTTPHost(firstBytes[:n], conn)
		if httpHost != "" {
			host = httpHost
			consumed = httpBytes
		} else {
			host = fmt.Sprintf("%s:%d", addr.IP, addr.Port)
		}
	}

	// Validate host against SSRF bypass patterns
	if err := security.ValidateHostIP(host); err != nil {
		log.Printf("WARN: invalid host format %q: %v", host, err)
		return host, consumed, false
	}

	allowed := p.matcher.IsAllowed(host)
	return host, consumed, allowed
}

// relay copies data between client and upstream bidirectionally.
// consumed bytes are written to upstream first (they were peeked during extraction).
func (p *TransparentProxy) relay(client, upstream net.Conn, consumed []byte) {
	var wg sync.WaitGroup
	wg.Add(2)

	// upstream <- client (plain copy)
	go func() {
		defer wg.Done()
		io.Copy(upstream, client)
		upstream.Close()
	}()

	// client <- upstream, but first write consumed bytes
	go func() {
		defer wg.Done()
		// Send consumed bytes first (TLS ClientHello or HTTP request line+headers)
		if len(consumed) > 0 {
			upstreamCopy := &peekReader{r: upstream, peeked: consumed}
			io.Copy(client, upstreamCopy)
			// Then continue with remaining upstream data
			io.Copy(client, upstream)
		} else {
			io.Copy(client, upstream)
		}
		client.Close()
	}()

	wg.Wait()
}

// peekReader wraps a reader and returns embedded bytes first, then reads from underlying.
type peekReader struct {
	r       net.Conn
	peeked  []byte
	peekIdx int
}

func (p *peekReader) Read(b []byte) (int, error) {
	if p.peekIdx < len(p.peeked) {
		n := copy(b, p.peeked[p.peekIdx:])
		p.peekIdx += n
		return n, nil
	}
	return p.r.Read(b)
}

// getOriginalDst retrieves the original destination for an iptables-redirected connection.
func getOriginalDst(conn net.Conn) (net.Addr, error) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return nil, fmt.Errorf("not a syscall.Conn")
	}

	rc, err := sc.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("SyscallConn: %w", err)
	}

	var origAddr unix.RawSockaddrAny
	var sockLen int32 = int32(unix.SizeofSockaddrAny)

	err = rc.Control(func(fd uintptr) {
		ret, _, _ := unix.Syscall6(
			unix.SYS_GETSOCKOPT,
			fd,
			uintptr(unix.IPPROTO_IP),
			uintptr(unix.SO_ORIGINAL_DST),
			uintptr(unsafe.Pointer(&origAddr)),
			uintptr(unsafe.Pointer(&sockLen)),
			0)
		if ret != 0 {
			err = syscall.Errno(ret)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("getsockopt SO_ORIGINAL_DST: %v", err)
	}

	if origAddr.Addr.Family == unix.AF_INET {
		ptr4 := (*unix.RawSockaddrInet4)(unsafe.Pointer(&origAddr))
		// Port is stored in network byte order — convert to host byte order
		port := int(binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&ptr4.Port))[:]))
		return &net.TCPAddr{
			IP:   net.IP(ptr4.Addr[:]),
			Port: port,
		}, nil
	} else if origAddr.Addr.Family == unix.AF_INET6 {
		ptr6 := (*unix.RawSockaddrInet6)(unsafe.Pointer(&origAddr))
		port := int(binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&ptr6.Port))[:]))
		return &net.TCPAddr{
			IP:   net.IP(ptr6.Addr[:]),
			Port: port,
		}, nil
	}

	return nil, fmt.Errorf("unknown address family: %d", origAddr.Addr.Family)
}
