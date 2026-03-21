package main

import (
	"io"
	"net"
)

// ExtractSNIFromClientHello extracts the Server Name Indication (SNI)
// from a TLS ClientHello message. It peeks at the ClientHello without
// terminating TLS. Returns all consumed bytes (for replay) and hostname.
func ExtractSNIFromClientHello(firstBytes []byte, conn net.Conn) ([]byte, string) {
	// TLS record header: content_type (1) + version (2) + length (2)
	// For ClientHello, content_type = 0x16 (handshake)
	if len(firstBytes) < 5 {
		return firstBytes, ""
	}

	// Read full handshake header: type (1) + length (3)
	handshakeHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, handshakeHeader); err != nil {
		return firstBytes, ""
	}

	// handshake type should be 0x01 (ClientHello)
	if handshakeHeader[0] != 0x01 {
		return firstBytes, ""
	}

	// Read ClientHello body
	// We read a reasonable chunk and scan for SNI extension (type 0x0000)
	body := make([]byte, 1024)
	n, _ := io.ReadFull(conn, body)

	// TLS ClientHello structure after random:
	// offset 0-33: client_version (2) + random (32)
	// offset 34: session_id_length (1)
	// offset 35+: session_id (variable)
	// Then: cipher_suites_length (2), cipher_suites (variable)
	// Then: compression_methods_length (1), compression_methods (variable)
	// Then: extensions_length (2)
	// Then: extensions (variable)

	offset := 34

	if offset >= n {
		return firstBytes, ""
	}

	// session_id_length (1)
	sessionIDLen := int(body[offset])
	offset += 1 + sessionIDLen

	if offset+2 >= n {
		return firstBytes, ""
	}

	// cipher_suites_length (2)
	cipherLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2 + cipherLen

	if offset >= n {
		return firstBytes, ""
	}

	// compression_methods_length (1)
	compressionLen := int(body[offset])
	offset += 1 + compressionLen

	if offset+2 >= n {
		return firstBytes, ""
	}

	// extensions_length (2)
	extensionsLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2

	if offset+extensionsLen > n {
		return firstBytes, ""
	}

	// Scan extensions for SNI (type 0x0000)
	extensionsEnd := offset + extensionsLen
	for offset+4 <= extensionsEnd {
		extType := int(body[offset])<<8 | int(body[offset+1])
		extLen := int(body[offset+2])<<8 | int(body[offset+3])
		offset += 4

		if extType == 0 { // SNI extension
			// SNI extension_data structure:
			//   server_name_list_length (2 bytes) — total length of following list
			//   For each entry:
			//     name_type (1 byte) — 0x00 = hostname
			//     name_length (2 bytes)
			//     name (name_length bytes)
			if offset+4 > extensionsEnd {
				return firstBytes, ""
			}
			// name_length is at body[offset+2] and body[offset+3]
			// (offset+0 = server_name_list_length high,
			//  offset+1 = server_name_list_length low,
			//  offset+2 = name_type, offset+3 = name_length high)
			nameLen := int(body[offset+2])<<8 | int(body[offset+3])
			if offset+4+nameLen > extensionsEnd {
				return firstBytes, ""
			}
			// name starts at offset+4 (after: list_len(2) + name_type(1) + name_len(2))
			consumed := append(handshakeHeader, body[:n]...)
			return consumed, string(body[offset+4 : offset+4+nameLen])
		}

		offset += extLen
	}

	return firstBytes, ""
}
