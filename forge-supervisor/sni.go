package main

import (
	"io"
	"net"
)

// ExtractSNIFromClientHello extracts the Server Name Indication (SNI)
// from a TLS ClientHello message. It peeks at the ClientHello without
// terminating TLS. Returns the hostname or empty string if not found.
func ExtractSNIFromClientHello(firstBytes []byte, conn net.Conn) string {
	// TLS record header: content_type (1) + version (2) + length (2)
	// For ClientHello, content_type = 0x16 (handshake), version = 0x03 0x01 (TLS 1.0)
	// We already checked that firstBytes[0] == 0x16 and firstBytes[1] == 0x03

	// We need to read more bytes to get the full ClientHello
	// The record length is in bytes 3-4 (big-endian)
	if len(firstBytes) < 5 {
		return ""
	}

	// ClientHello body starts after the record header (5 bytes)
	// Read the handshake header: type (1) + length (3)
	handshakeHeader := make([]byte, 4)
	_, err := io.ReadFull(conn, handshakeHeader)
	if err != nil {
		return ""
	}

	// handshake type should be 0x01 (ClientHello)
	if handshakeHeader[0] != 0x01 {
		return ""
	}

	// handshake length (big-endian, 3 bytes)
	handshakeLen := int(handshakeHeader[1])<<16 | int(handshakeHeader[2])<<8 | int(handshakeHeader[3])

	// Read ClientHello body
	// We need: client_version (2) + random (32) + session_id_len (1) + cipher_suites_len (2) + ...
	// Skip to find the SNI extension (extension type 0x0000)
	// This is complex, so we read a reasonable chunk and scan for SNI

	bodyLen := handshakeLen
	if bodyLen > 1024 { // Sanity limit
		bodyLen = 1024
	}

	body := make([]byte, bodyLen)
	n, err := io.ReadFull(conn, body)
	if err != nil {
		return ""
	}

	// TLS ClientHello structure after random:
	// session_id_length (1 byte)
	// cipher_suites_length (2 bytes)
	// cipher_suites (variable)
	// compression_methods_length (1 byte)
	// compression_methods (variable)
	// extensions_length (2 bytes)
	// extensions (variable) <- SNI is here with type 0x0000

	offset := 0

	// client_version (2) + random (32) = 34 bytes
	offset += 34

	if offset >= n {
		return ""
	}

	// session_id_length (1)
	sessionIDLen := int(body[offset])
	offset += 1 + sessionIDLen

	if offset >= n {
		return ""
	}

	// cipher_suites_length (2)
	cipherLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2 + cipherLen

	if offset >= n {
		return ""
	}

	// compression_methods_length (1)
	compressionLen := int(body[offset])
	offset += 1 + compressionLen

	if offset >= n {
		return ""
	}

	// extensions_length (2)
	extensionsLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2

	if offset+extensionsLen > n {
		return ""
	}

	// Scan extensions for SNI (type 0x0000)
	extensionsEnd := offset + extensionsLen
	for offset+4 <= extensionsEnd {
		extType := int(body[offset])<<8 | int(body[offset+1])
		extLen := int(body[offset+2])<<8 | int(body[offset+3])
		offset += 4

		if extType == 0 { // SNI extension
			// SNI value: list_length (1) + name_type (1) + name_length (2) + name
			if offset+4 > extensionsEnd {
				return ""
			}
			// Skip server_name list (first byte is list length, then name_type, then name_length)
			nameLen := int(body[offset+2])<<8 | int(body[offset+3])
			if offset+4+nameLen > extensionsEnd {
				return ""
			}
			return string(body[offset+4 : offset+4+nameLen])
		}

		offset += extLen
	}

	return ""
}
