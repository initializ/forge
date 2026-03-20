package main

import (
	"context"
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

// TransparentProxy is a transparent TCP proxy that intercepts redirected traffic,
// extracts the target hostname (via SNI or HTTP Host header), checks against
// the domain matcher, and either forwards or denies the connection.
type TransparentProxy struct {
	listener      net.Listener
	matcher       *security.DomainMatcher
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
		matcher:       matcher,
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

	// Get the original destination from the redirected connection
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

	// Extract the target host from the connection
	host, allowed := p.extractAndCheck(client, origTCPAddr)

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

		// Send a simple "connection denied" message and close
		fmt.Fprintf(client, "HTTP/1.1 403 Forbidden\r\n\r\n")
		return
	}

	// Log the allowed connection
	p.audit.Log(&AuditEvent{
		Timestamp: time.Now().UTC(),
		Action:    "allowed",
		Host:      host,
		Port:      origTCPAddr.Port,
	})

	// Dial the original destination
	upstream, err := net.DialTimeout("tcp", origTCPAddr.String(), 10*time.Second)
	if err != nil {
		log.Printf("ERROR: dial upstream %s: %v", origTCPAddr, err)
		return
	}
	defer upstream.Close()

	// Relay data between client and upstream
	p.relay(client, upstream)
}

// extractAndCheck reads the initial bytes from the connection to extract the
// target hostname and checks it against the matcher.
func (p *TransparentProxy) extractAndCheck(conn net.Conn, addr *net.TCPAddr) (string, bool) {
	// Peek at the first few bytes to determine if this is TLS or HTTP
	firstBytes := make([]byte, 5)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	n, err := conn.Read(firstBytes)
	if err != nil && err != io.EOF {
		log.Printf("ERROR: peek bytes: %v", err)
		return fmt.Sprintf("%s:%d", addr.IP, addr.Port), false
	}

	var host string

	if n >= 3 && firstBytes[0] == 0x16 && firstBytes[1] == 0x03 {
		// TLS ClientHello
		host = ExtractSNIFromClientHello(firstBytes[:n], conn)
		if host == "" {
			host = fmt.Sprintf("%s:%d", addr.IP, addr.Port)
		}
	} else {
		// HTTP request - read the Host header
		host = ExtractHTTPHost(conn, firstBytes[:n])
		if host == "" {
			host = fmt.Sprintf("%s:%d", addr.IP, addr.Port)
		}
	}

	// Validate the host before checking
	if err := security.ValidateHostIP(host); err != nil {
		log.Printf("WARN: invalid host format %q: %v", host, err)
		return host, false
	}

	// Check if the host is allowed
	allowed := p.matcher.IsAllowed(host)
	return host, allowed
}

// relay copies data between client and upstream bidirectionally.
func (p *TransparentProxy) relay(client, upstream net.Conn) {
	buf := make([]byte, 32*1024)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.CopyBuffer(upstream, client, buf)
		upstream.Close()
	}()

	go func() {
		defer wg.Done()
		io.CopyBuffer(client, upstream, buf)
		client.Close()
	}()

	wg.Wait()
}

// getOriginalDst retrieves the original destination address for a
// connection that was redirected via iptables REDIRECT.
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
		return &net.TCPAddr{
			IP:   net.IP(ptr4.Addr[:]),
			Port: int(ptr4.Port),
		}, nil
	} else if origAddr.Addr.Family == unix.AF_INET6 {
		ptr6 := (*unix.RawSockaddrInet6)(unsafe.Pointer(&origAddr))
		return &net.TCPAddr{
			IP:   net.IP(ptr6.Addr[:]),
			Port: int(ptr6.Port),
		}, nil
	}

	return nil, fmt.Errorf("unknown address family: %d", origAddr.Addr.Family)
}
