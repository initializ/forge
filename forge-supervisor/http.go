package main

import (
	"bufio"
	"io"
	"net"
	"strings"
)

// ExtractHTTPHost extracts the Host header from an HTTP request.
// It reads just enough to find the Host header without consuming the body.
func ExtractHTTPHost(conn net.Conn, initialBytes []byte) string {
	// We have the first few bytes from the TLS detection
	// If it's HTTP, we need to read lines until we find Host

	// Combine initial bytes with a buffered reader
	reader := bufio.NewReader(io.MultiReader(
		strings.NewReader(string(initialBytes)),
		conn,
	))

	// Read request line (we don't need it, but we must consume it)
	_, _ = reader.ReadString('\n')

	// Read headers
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return ""
		}

		// End of headers
		if line == "\r\n" || line == "\n" {
			break
		}

		// Check for Host header
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			host := strings.TrimSpace(line[4:]) // Remove "host:" prefix
			// Remove trailing \r\n
			host = strings.TrimSuffix(host, "\r")
			host = strings.TrimSuffix(host, "\n")
			// Remove port if present
			if idx := strings.Index(host, ":"); idx != -1 {
				host = host[:idx]
			}
			return strings.ToLower(host)
		}
	}

	return ""
}
