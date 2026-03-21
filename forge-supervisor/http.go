package main

import (
	"bufio"
	"io"
	"net"
	"strings"
)

// ExtractHTTPHost reads an HTTP request from the connection (using initialBytes
// as the start) to find the Host header. Returns consumed bytes (for replay)
// and the hostname.
func ExtractHTTPHost(initialBytes []byte, conn net.Conn) ([]byte, string) {
	reader := bufio.NewReader(io.MultiReader(
		strings.NewReader(string(initialBytes)),
		conn,
	))

	// Read and consume request line
	_, err := reader.ReadString('\n')
	if err != nil {
		return initialBytes, ""
	}

	// Read headers
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return initialBytes, ""
		}

		// End of headers
		if line == "\r\n" || line == "\n" {
			break
		}

		// Host: header — line is "Host: value\r\n"
		// "Host:" is 5 characters
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			host := strings.TrimSpace(line[5:]) // Skip "Host:" (5 chars)
			host = strings.TrimSuffix(host, "\r")
			host = strings.ToLower(host)
			// Remove port if present
			if idx := strings.Index(host, ":"); idx != -1 {
				host = host[:idx]
			}
			// Consume all bytes up to and including headers
			consumed := append([]byte(line), []byte("\r\n")...)
			return consumed, host
		}
	}

	return initialBytes, ""
}
